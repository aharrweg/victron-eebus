package main

import (
    "fmt"
    "time"
)

func main() {
    fmt.Println("EEBUS Cerbo Service gestartet...")
    for {
        time.Sleep(10 * time.Second)
    }
}
