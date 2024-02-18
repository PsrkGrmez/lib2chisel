package main

import (
	"C"
	"flag"
	"log"
)

func main() {
	log.Println("Chisel is Starting...")
	svType := flag.String("type", "", "listen host:port")
	server := flag.String("server", "0.0.0.0", "destination host:port")
	point := flag.String("point", "127.0.0.1:5050:127.0.0.1:443", "destination host:port")
	flag.Parse()

	var relay IService
	relay = NewTunnel(*svType, *server, *point)
	relay.Start()
}
