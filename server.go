package gitwatch

import (
	"crypto/rand"
	fmt "fmt"
	"log"
	unsafe_rand "math/rand"
	"net"
	"net/http"
	"sync"

	"github.com/google/go-github/github"
	"google.golang.org/grpc"
)

// A MissingUserErr indicates a user was not found
type MissingUserErr struct {
	user uint64
}

func (e MissingUserErr) Error() string {
	return fmt.Sprintf("user %d is not found", e.user)
}

type Subscription struct {
	stream Github_SubscribeServer
	id     uint64
}

// A Server listens for POSTs from the github hooks API and gRPC calls
type Server struct {
	lock          sync.RWMutex
	client        *github.Client
	target        string
	subscriptions map[string][]Subscription
	secret        []byte
	grpcAddr      string
}

// GetSubscriptions gets all subscribers to event on a repo.
func (hl *Server) GetSubscriptions(repo string, event string) []Subscription {
	return hl.subscriptions[repo+"#"+event]
}

// GetSecret gets a secret
func (hl *Server) GetSecret() []byte {
	return hl.secret
}

// AddSubscription subscribes a user to a repo event
func (hl *Server) AddSubscription(repo string, event string, sub Subscription) error {
	log.Printf("[method AddSubscription] status, msg = %q, repo = %q, event = %q, id = \"%d\" ", "adding subscriber", repo, event, sub.id)

	hl.lock.Lock()
	subs := hl.GetSubscriptions(repo, event)
	if subs == nil {
		hl.subscriptions[repo+"#"+event] = []Subscription{sub}
	} else {
		hl.subscriptions[repo+"#"+event] = append(subs, sub)
	}
	hl.lock.Unlock()
	return nil
}

// RemoveSubscription removes a user subscription to a event.
func (hl *Server) RemoveSubscription(repo string, event string, user uint64) error {
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

func (hl *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var ev Event
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

		ev = Event{
			Repo: &Repo{
				Id:   event.GetRepo().GetID(),
				Name: event.GetRepo().GetFullName(),
			},
			User: &User{
				Id:   event.GetPusher().GetID(),
				Name: event.GetPusher().GetName(),
			},
			Payload: &Event_Push{
				Push: &PushPayload{
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
		ev = Event{
			Repo: &Repo{
				Id:   event.GetRepo().GetID(),
				Name: event.GetRepo().GetFullName(),
			},
			User: &User{
				Id:   event.GetSender().GetID(),
				Name: event.GetSender().GetName(),
			},
			Payload: &Event_PullRequest{
				PullRequest: &PullRequestPayload{
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
func (hl *Server) GenID() uint64 {
	return unsafe_rand.Uint64()
}

// Subscribe is for an implementation of the github proto service
func (hl *Server) Subscribe(target *Target, stream Github_SubscribeServer) error {
	log.Printf("[grpc-method Subscribe] status, msg = %q", "subscription requested")

	return hl.AddSubscription(target.User+"/"+target.Repo, EventTypeToString(target.Event), Subscription{
		stream: stream,
		id:     hl.GenID(),
	})
}

// NewServer initializes a hook listener with no users
func NewServer(hookTarget, grpcAddr string) Server {
	secretBuf := make([]byte, 20)
	rand.Read(secretBuf)
	return Server{
		grpcAddr:      grpcAddr,
		lock:          sync.RWMutex{},
		target:        hookTarget,
		client:        github.NewClient(nil),
		subscriptions: make(map[string][]Subscription),
		secret:        secretBuf,
	}
}

// PullRequestActionFromString translates a pull request action string from the github api to the protobuf enum
func PullRequestActionFromString(pra string) PullRequestAction {
	switch pra {
	case "assigned":
		return PullRequestAction_ASSIGNED
	case "unassigned":
		return PullRequestAction_UNASSIGNED
	case "review_requested":
		return PullRequestAction_REVIEW_REQUESTED
	case "review_request_removed":
		return PullRequestAction_REVIEW_REQUEST_REMOVED
	case "labeled":
		return PullRequestAction_LABELED
	case "unlabeled":
		return PullRequestAction_UNLABELED
	case "opened":
		return PullRequestAction_OPENED
	case "closed":
		return PullRequestAction_CLOSED
	case "reopened":
		return PullRequestAction_REOPENED
	}
	return -1
}

// PullRequestStateFromString translates a pull request state string from the github api to the protobuf enum
func PullRequestStateFromString(prs string) PullRequestState {
	switch prs {
	case "opened":
		return PullRequestState_PR_OPENED
	case "closed":
		return PullRequestState_PR_CLOSED
	}
	return -1
}

func (hl *Server) ListenAndServe() (err error) {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		err = http.ListenAndServe(hl.target, hl)
		if err != nil {
			return
		}
		wg.Done()
	}()

	go func() {
		var listener net.Listener
		listener, err = net.Listen("tcp", hl.grpcAddr)
		if err != nil {
			return
		}
		srv := grpc.NewServer()
		RegisterGithubServer(srv, hl)
		err = srv.Serve(listener)
		if err != nil {
			return
		}
		wg.Done()
	}()
	wg.Wait()
	return
}

func EventTypeFromString(evt string) EventType {
	switch evt {
	case "pull_request":
		return EventType_PULL_REQUEST
	case "push":
		return EventType_PUSH
	}
	return -1
}

func EventTypeToString(evt EventType) string {
	switch evt {
	case EventType_PULL_REQUEST:
		return "pull_request"
	case EventType_PUSH:
		return "push"
	}
	return ""
}
