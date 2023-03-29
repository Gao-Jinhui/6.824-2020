package mr

import (
	"fmt"
)

type BitMap []uint64

const Size = 64

func NewBitMap(n int) BitMap {
	return make([]uint64, n/Size+1)
}

func (bt *BitMap) Set(n uint) {
	if n/Size > uint(len(*bt)) {
		fmt.Println("大小超出bitmap范围")
		return
	}
	byteIndex := n / Size                //第x个字节（0,1,2...）
	offsetIndex := n % Size              //偏移量(0<偏移量<byteSize)
	(*bt)[byteIndex] ^= 1 << offsetIndex //异或1（置位）
}

func (bt *BitMap) AllTasksDone() bool {
	for _, num := range *bt {
		if num != 0 {
			return false
		}
	}
	return true
}
