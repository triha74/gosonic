package main

import "fmt"

func Greet(name string) string {
	if name == "" {
		name = "World"
	}
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	fmt.Println(Greet(""))
}
