package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	unsafe_rand "math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"

	pb "github.com/IanS5/services/gitview"
	"github.com/google/go-github/github"
	"google.golang.org/grpc"
)

// NAME is the name used for subscriptions to github webhooks
var NAME = "web"

// A MissingUserErr indicates a user was not found
type MissingUserErr struct {
	user uint64
}

func (e MissingUserErr) Error() string {
	return fmt.Sprintf("user %d is not found", e.user)
}

type Subscription struct {
	stream pb.Github_SubscribeServer
	id     uint64
}

// A HookListener listens for POSTs from the github hooks API
type HookListener struct {
	lock          sync.RWMutex
	client        *github.Client
	target        string
	subscriptions map[string][]Subscription
	secret        []byte
}

// GetSubscriptions gets all subscribers to event on a repo.
func (hl *HookListener) GetSubscriptions(repo string, event string) []Subscription {
	return hl.subscriptions[repo+"#"+event]
}

// GetSecret gets a secret
func (hl *HookListener) GetSecret() []byte {
	return hl.secret
}

// AddSubscription subscribes a user to a repo event
func (hl *HookListener) AddSubscription(repo string, event string, sub Subscription) error {
	log.Printf("[method AddSubscription] status, msg = %q, repo = %q, event = %q, id = \"%d\" ", "adding subscriber", repo, event, sub.id)

	hl.lock.Lock()
	subs := hl.GetSubscriptions(repo, event)
	if subs == nil {
		repoParts := strings.Split(repo, "/")
		_, _, err := hl.client.Repositories.CreateHook(context.TODO(), repoParts[0], repoParts[1], &github.Hook{
			Name:   &NAME,
			URL:    &hl.target,
			Events: []string{event},
			Config: map[string]interface{}{
				"url":    hl.target,
				"secret": hl.GetSecret(),
			},
		})
		if err != nil {
			return err
		}
		hl.subscriptions[repo+"#"+event] = []Subscription{sub}
	} else {
		hl.subscriptions[repo+"#"+event] = append(subs, sub)
	}
	hl.lock.Unlock()
	return nil
}

// RemoveSubscription removes a user subscription to a event.
func (hl *HookListener) RemoveSubscription(repo string, event string, user uint64) error {
	log.Printf("[method RemoveSubscription] status, msg = %q, repo = %q, event = %q, id = \"%d\"", "removing subscriber", repo, event, user)

	hl.lock.Lock()
	subSlice := hl.GetSubscriptions(repo, event)
	if subSlice != nil {
		for i, u := range subSlice {
			if u.id == user {
				hl.subscriptions[repo+"#"+event] = append(subSlice[i:], subSlice[:i+1]...)
				hl.lock.Unlock()
				return nil
			}
		}
	}
	hl.lock.Unlock()
	return MissingUserErr{
		user: user,
	}
}

func (hl *HookListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqid := hl.GenID()
	log.Printf("[req %d] status, msg = %q user-agent = %q, remote = %q", reqid, "initialize", r.UserAgent(), r.RemoteAddr)

	payload, err := github.ValidatePayload(r, hl.GetSecret())
	if err != nil {
		log.Printf("[req %d] error, msg = \"%v\", response = %q, status = \"%d\", fatal = %q", reqid, err, "invalid payload", 400, "false")
		http.Error(w, "invalid payload", 400)
		return
	}
	typ := github.WebHookType(r)
	event, err := github.ParseWebHook(typ, payload)
	if err != nil {
		log.Printf("[req %d] error, msg = \"%v\", response = %q, status = \"%d\", fatal = %q", reqid, err, "invalid payload", 400, "false")
		http.Error(w, "invalid payload", 400)
		return
	}
	var ev pb.Event
	var subscribers []Subscription
	log.Printf("[req %d] status, msg = %q", reqid, "successfully parsed hook")
	switch event := event.(type) {
	case *github.PushEvent:
		subscribers = hl.GetSubscriptions(event.GetRepo().GetFullName(), "push")
		if subscribers == nil {
			log.Printf("[req %d] error, msg = \"%v\", response = %q, status = \"%d\", fatal = %q", reqid, "no subscribers to event", "(null)", 200, "false")
			return
		}
		log.Printf("[req %d] status, msg = %q, event = %q, repo = %q, sub-count = \"%d\"",
			reqid,
			"sending event to subscribers", "push",
			event.GetRepo().GetFullName(),
			len(subscribers))

		ev = pb.Event{
			Repo: &pb.Repo{
				Id:   event.GetRepo().GetID(),
				Name: event.GetRepo().GetFullName(),
			},
			User: &pb.User{
				Id:   event.GetPusher().GetID(),
				Name: event.GetPusher().GetName(),
			},
			Payload: &pb.Event_Push{
				Push: &pb.PushPayload{
					Ref:    event.GetRef(),
					Before: event.GetBefore(),
					Head:   event.GetHead(),
				},
			},
		}

	case *github.PullRequestEvent:
		subscribers = hl.GetSubscriptions(event.GetRepo().GetFullName(), "pull_request")
		if subscribers == nil {
			log.Printf("[req %d] error, msg = \"%v\", response = %q, status = \"%d\", fatal = %q", reqid, "no subscribers to event", "(null)", 200, "false")
			return
		}
		log.Printf("[req %d] status, msg = %q, event = %q, repo = %q, sub-count = \"%d\"",
			reqid,
			"sending event to subscribers", "pull_request",
			event.GetRepo().GetFullName(),
			len(subscribers))
		ev = pb.Event{
			Repo: &pb.Repo{
				Id:   event.GetRepo().GetID(),
				Name: event.GetRepo().GetFullName(),
			},
			User: &pb.User{
				Id:   event.GetSender().GetID(),
				Name: event.GetSender().GetName(),
			},
			Payload: &pb.Event_PullRequest{
				PullRequest: &pb.PullRequestPayload{
					Action:   PullRequestActionFromString(event.GetAction()),
					Number:   int32(event.GetNumber()),
					State:    PullRequestStateFromString(event.GetPullRequest().GetState()),
					Id:       event.PullRequest.GetID(),
					Title:    event.PullRequest.GetTitle(),
					DiffUrl:  event.PullRequest.GetDiffURL(),
					PatchUrl: event.PullRequest.GetPatchURL(),
					Body:     event.PullRequest.GetBody(),
				},
			},
		}
	default:
		http.Error(w, "invalid payload", 400)
		return
	}
	for _, sub := range subscribers {
		log.Printf("[req %d] status, msg = %q, subscriber = \"%d\"", reqid, "sending event to subscriber", sub.id)

		if err = sub.stream.Send(&ev); err != nil {
			log.Printf("[req %d] warn, msg = \"%v\"", reqid, err)
		}
	}
}

// GenID generates a new 64 bit ID for a subscription
func (hl *HookListener) GenID() uint64 {
	return unsafe_rand.Uint64()
}

// Subscribe is for an implementation of the github proto service
func (hl *HookListener) Subscribe(target *pb.Target, stream pb.Github_SubscribeServer) error {
	log.Printf("[grpc-method Subscribe] status, msg = %q", "subscription requested")

	return hl.AddSubscription(target.Repo, target.User, Subscription{
		stream: stream,
		id:     hl.GenID(),
	})
}

// NewHookListener initializes a hook listener with no users
func NewHookListener(target string) HookListener {
	secretBuf := make([]byte, 20)
	rand.Read(secretBuf)
	return HookListener{
		lock:          sync.RWMutex{},
		target:        target,
		client:        github.NewClient(nil),
		subscriptions: make(map[string][]Subscription),
		secret:        secretBuf,
	}
}

// PullRequestActionFromString translates a pull request action string from the github api to the protobuf enum
func PullRequestActionFromString(pra string) pb.PullRequestAction {
	switch pra {
	case "assigned":
		return pb.PullRequestAction_ASSIGNED
	case "unassigned":
		return pb.PullRequestAction_UNASSIGNED
	case "review_requested":
		return pb.PullRequestAction_REVIEW_REQUESTED
	case "review_request_removed":
		return pb.PullRequestAction_REVIEW_REQUEST_REMOVED
	case "labeled":
		return pb.PullRequestAction_LABELED
	case "unlabeled":
		return pb.PullRequestAction_UNLABELED
	case "opened":
		return pb.PullRequestAction_OPENED
	case "closed":
		return pb.PullRequestAction_CLOSED
	case "reopened":
		return pb.PullRequestAction_REOPENED
	}
	return -1
}

// PullRequestStateFromString translates a pull request state string from the github api to the protobuf enum
func PullRequestStateFromString(prs string) pb.PullRequestState {
	switch prs {
	case "opened":
		return pb.PullRequestState_PR_OPENED
	case "closed":
		return pb.PullRequestState_PR_CLOSED
	}
	return -1
}

// StartGRPC starts the gRPC server
func StartGRPC(addr string, handler *HookListener) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	pb.RegisterGithubServer(srv, handler)
	return srv.Serve(listener)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("USAGE: %s [HOOK ADDRESS] [GRPC ADDRESS]", os.Args[0])
		os.Exit(1)
	}
	handler := NewHookListener(os.Args[1])
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		tid := runtime.NumGoroutine()
		err := http.ListenAndServe(os.Args[1], &handler)
		if err != nil {
			log.Fatalf("[goroutine %d] error, msg = \"%v\", fatal = %q", tid, err, "true")
		}
		wg.Done()
	}()

	go func() {
		tid := runtime.NumGoroutine()
		err := StartGRPC(os.Args[2], &handler)
		if err != nil {
			log.Fatalf("[goroutine %d] error, msg = \"%v\", fatal = %q", tid, err, "true")
		}
		wg.Done()
	}()
	wg.Wait()
	fmt.Println("done")
}
