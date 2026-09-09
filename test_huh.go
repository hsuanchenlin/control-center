package main

import (
	"fmt"
	"github.com/charmbracelet/huh"
)

func main() {
	holder := new(string)
	*holder = "hello"
	input := huh.NewInput().Value(holder)
	fmt.Printf("%T\n", input.GetValue())
}
