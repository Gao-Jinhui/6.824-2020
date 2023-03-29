package mr

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sync"
	"time"
)

type MetaState int

const (
	Idle MetaState = iota
	InProgress
	Completed
)

type State int

const (
	Map State = iota
	Reduce
	Exit
	Wait
)

type Master struct {
	TaskChannel   chan *Task        // 等待执行的task
	MetaTaskMap   map[int]*MetaTask // 当前所有task的信息
	MasterPhase   State             // Master的阶段
	NReduce       int
	InputFiles    []string
	Intermediates [][]string // Map任务产生的R个中间文件的信息
	BitMap        BitMap
}

type MetaTask struct {
	MetaState     MetaState
	StartTime     time.Time
	TaskReference *Task
}

type Task struct {
	Input         string
	TaskState     State
	NReducer      int
	TaskNumber    int
	Intermediates []string
	Output        string
}

var mu sync.Mutex

//
// start a thread that listens for RPCs from worker.go
//
func (m *Master) server() {
	rpc.Register(m)
	rpc.HandleHTTP()
	//l, e := net.Listen("tcp", ":1234")
	sockname := masterSock()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

//
// main/mrmaster.go calls Exit() periodically to find out
// if the entire job has finished.
//
func (m *Master) Done() bool {
	mu.Lock()
	defer mu.Unlock()
	ret := m.MasterPhase == Exit
	return ret
}

//
// create a Master.
// main/mrmaster.go calls this function.
// nReduce is the number of reduce tasks to use.
//
func MakeMaster(files []string, nReduce int) *Master {
	m := Master{
		TaskChannel:   make(chan *Task, max(nReduce, len(files))),
		MetaTaskMap:   make(map[int]*MetaTask),
		MasterPhase:   Map,
		NReduce:       nReduce,
		InputFiles:    files,
		Intermediates: make([][]string, nReduce),
	}

	// 切成16MB-64MB的文件
	// 创建map任务
	m.createMapTask()

	// 一个程序成为master，其他成为worker
	//这里就是启动master 服务器就行了，
	//拥有master代码的就是master，别的发RPC过来的都是worker
	m.server()
	// 启动一个goroutine 检查超时的任务
	go m.catchTimeOut()
	return &m
}

func (m *Master) catchTimeOut() {
	for {
		time.Sleep(5 * time.Second)
		mu.Lock()
		if m.MasterPhase == Exit {
			mu.Unlock()
			return
		}
		for _, taskMeta := range m.MetaTaskMap {
			if taskMeta.MetaState == InProgress && time.Now().Sub(taskMeta.StartTime) > 10*time.Second {
				m.TaskChannel <- taskMeta.TaskReference
				taskMeta.MetaState = Idle
			}
		}
		mu.Unlock()
	}
}

func (m *Master) createMapTask() {
	m.BitMap = NewBitMap(len(m.InputFiles))
	// 根据传入的filename，每个文件对应一个map task
	for idx, filename := range m.InputFiles {
		taskMeta := Task{
			Input:      filename,
			TaskState:  Map,
			NReducer:   m.NReduce,
			TaskNumber: idx,
		}
		m.TaskChannel <- &taskMeta
		m.MetaTaskMap[idx] = &MetaTask{
			MetaState:     Idle,
			TaskReference: &taskMeta,
		}
		m.BitMap.Set(uint(idx))
	}
	fmt.Println(m.BitMap)
}

func (m *Master) createReduceTask() {
	m.BitMap = NewBitMap(len(m.Intermediates))
	m.MetaTaskMap = make(map[int]*MetaTask)
	for idx, files := range m.Intermediates {
		task := Task{
			TaskState:     Reduce,
			NReducer:      m.NReduce,
			TaskNumber:    idx,
			Intermediates: files,
		}
		m.TaskChannel <- &task
		m.MetaTaskMap[idx] = &MetaTask{
			MetaState:     Idle,
			TaskReference: &task,
		}
		m.BitMap.Set(uint(idx))
	}
	fmt.Println(m.BitMap)
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *Master) AssignTask(args *ExampleArgs, reply *Task) error {
	mu.Lock()
	defer mu.Unlock()
	if len(m.TaskChannel) > 0 {
		*reply = *<-m.TaskChannel
		m.MetaTaskMap[reply.TaskNumber].MetaState = InProgress
		m.MetaTaskMap[reply.TaskNumber].StartTime = time.Now()
	} else if m.MasterPhase == Exit {
		*reply = Task{TaskState: Exit}
	} else {
		// 没有task就让worker 等待
		*reply = Task{TaskState: Wait}
	}
	return nil
}

func (m *Master) TaskCompleted(task *Task, reply *ExampleReply) error {
	//更新task状态
	mu.Lock()
	defer mu.Unlock()
	if task.TaskState != m.MasterPhase || m.MetaTaskMap[task.TaskNumber].MetaState == Completed {
		// 因为worker写在同一个文件磁盘上，对于重复的结果要丢弃
		return nil
	}
	m.MetaTaskMap[task.TaskNumber].MetaState = Completed
	go m.processTaskResult(task)
	return nil
}

func (m *Master) processTaskResult(task *Task) {
	mu.Lock()
	defer mu.Unlock()
	switch task.TaskState {
	case Map:
		m.BitMap.Set(uint(task.TaskNumber))
		//收集intermediate信息
		for reduceTaskId, filePath := range task.Intermediates {
			m.Intermediates[reduceTaskId] = append(m.Intermediates[reduceTaskId], filePath)
		}
		if m.BitMap.AllTasksDone() {
			//获得所以map task后，进入reduce阶段
			m.createReduceTask()
			m.MasterPhase = Reduce
		}
	case Reduce:
		m.BitMap.Set(uint(task.TaskNumber))
		if m.BitMap.AllTasksDone() {
			//获得所以reduce task后，进入exit阶段
			m.MasterPhase = Exit
		}
	}
}
