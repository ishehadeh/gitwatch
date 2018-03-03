package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"google.golang.org/grpc"

	pb "github.com/IanS5/gitwatch"
)

func usage() {
	fmt.Printf("USAGE: %s [server|client] ARGS...\n", os.Args[0])
	fmt.Printf("SUBCOMMANDS:\n")
	fmt.Printf("\tclient ADDR USER REPO EVENT\n")
	fmt.Printf("\t\ttell the gitwatch server at ADDR to listen for EVENT from USER's REPO, then wait for responses\n")
	fmt.Printf("\tserver HOOKADDR GRPCADDR\n")
	fmt.Printf("\t\tlisten for grpc requests on GRPCADDR, and listen for webhooks on HOOKADDR\n")
}
func main() {
	if len(os.Args) < 4 {
		usage()
		os.Exit(1)
	}
	if os.Args[1][0] == 's' {
		handler := pb.NewServer(os.Args[2], os.Args[3])
		log.Fatal(handler.ListenAndServe())
	} else if os.Args[1][0] == 'c' {
		if len(os.Args) < 6 {
			usage()
			os.Exit(1)
		}
		conn, err := grpc.Dial(os.Args[2], grpc.WithInsecure())
		if err != nil {
			log.Fatal(err)
		}
		client := pb.NewGithubClient(conn)
		eventType := pb.EventTypeFromString(os.Args[5])
		if eventType < 0 {
			log.Fatal("event not supported")
		}
		subscription, err := client.Subscribe(context.TODO(), &pb.Target{
			User:  os.Args[3],
			Repo:  os.Args[4],
			Event: eventType,
		})
		if err != nil {
			log.Fatal(err)
		}
		for true {
			event, err := subscription.Recv()
			if err == io.EOF {
				continue
			}
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(*event)
		}
	}
}
