package btrdb

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pborman/uuid"
	pb "gopkg.in/btrdb.v4/grpcinterface"
)

//ErrorDisconnected is returned when operations are attempted after Disconnect()
//is called.
var ErrorDisconnected = &CodedError{&pb.Status{Code: 421, Msg: "Driver is disconnected"}}

//ErrorClusterDegraded is returned when a write operation on an unmapped UUID is attempted.
//generally the same operation will succeed if attempted once the cluster has recovered.
var ErrorClusterDegraded = &CodedError{&pb.Status{Code: 419, Msg: "Cluster is degraded"}}

//ErrorWrongArgs is returned from API functions if the parameters are nonsensical
var ErrorWrongArgs = &CodedError{&pb.Status{Code: 421, Msg: "Invalid Arguments"}}

//BTrDB is the main object you should use to interact with BTrDB.
type BTrDB struct {
	//This covers the mash
	mashwmu    sync.Mutex
	activeMash atomic.Value

	closed bool

	//This covers the epcache
	epmu    sync.RWMutex
	epcache map[uint32]*Endpoint
}

func newBTrDB() *BTrDB {
	return &BTrDB{epcache: make(map[uint32]*Endpoint)}
}

//StatPoint represents a statistical summary of a window. The length of that
//window must be determined from context (e.g the parameters passed to AlignedWindow or Window methods)
type StatPoint struct {
	//The time of the start of the window, in nanoseconds since the epoch UTC
	Time  int64
	Min   float64
	Mean  float64
	Max   float64
	Count uint64
}

//Connect takes a list of endpoints and returns a BTrDB handle.
//Note that only a single endpoint is technically required, but having
//more endpoints will make the initial connection more robust to cluster
//changes. Different addresses for the same endpoint are permitted
func Connect(ctx context.Context, endpoints ...string) (*BTrDB, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("No endpoints provided")
	}
	b := newBTrDB()
	for _, epa := range endpoints {
		ep, err := ConnectEndpoint(ctx, epa)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			continue
		}
		mash, err := ep.Info(ctx)
		if err != nil {
			continue
		}
		b.activeMash.Store(mash)
		break
	}
	if b.activeMash.Load() == nil {
		return nil, fmt.Errorf("Could not connect to cluster via provided endpoints")
	}
	return b, nil
}

//Disconnect will close all active connections to the cluster. All future calls
//will return ErrorDisconnected
func (b *BTrDB) Disconnect() error {
	b.epmu.Lock()
	defer b.epmu.Unlock()
	var gerr error
	for _, ep := range b.epcache {
		err := ep.Disconnect()
		if err != nil {
			gerr = err
		}
	}
	b.closed = true
	return gerr
}

//EndpointForHash is a low level function that returns a single endpoint for an
//endpoint hash.
func (b *BTrDB) EndpointForHash(ctx context.Context, hash uint32) (*Endpoint, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m := b.activeMash.Load().(*MASH)
	b.epmu.RLock()
	ep, ok := b.epcache[hash]
	b.epmu.RUnlock()
	if ok {
		return ep, nil
	}
	var addrs []string
	for _, ep := range m.eps {
		if ep.hash == hash {
			addrs = ep.grpc
		}
	}
	//We need to connect to endpoint
	nep, err := ConnectEndpoint(ctx, addrs...)
	if err != nil {
		return nil, err
	}
	b.epmu.Lock()
	b.epcache[hash] = nep
	b.epmu.Unlock()
	return nep, nil
}

//ReadEndpointFor returns the endpoint that should be used to read the given uuid
func (b *BTrDB) ReadEndpointFor(ctx context.Context, uuid uuid.UUID) (*Endpoint, error) {
	//TODO do rpref
	return b.EndpointFor(ctx, uuid)
}

//EndpointFor returns the endpoint that should be used to write the given uuid
func (b *BTrDB) EndpointFor(ctx context.Context, uuid uuid.UUID) (*Endpoint, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m := b.activeMash.Load().(*MASH)
	ok, hash, addrs := m.EndpointFor(uuid)
	if !ok {
		return nil, ErrorClusterDegraded
	}
	b.epmu.RLock()
	ep, ok := b.epcache[hash]
	b.epmu.RUnlock()
	if ok {
		return ep, nil
	}
	//We need to connect to endpoint
	nep, err := ConnectEndpoint(ctx, addrs...)
	if err != nil {
		return nil, err
	}
	b.epmu.Lock()
	b.epcache[hash] = nep
	b.epmu.Unlock()
	return nep, nil
}

func (b *BTrDB) getAnyEndpoint(ctx context.Context) (*Endpoint, error) {
	b.epmu.RLock()
	for _, ep := range b.epcache {
		b.epmu.RUnlock()
		return ep, nil
	}
	b.epmu.RUnlock()
	//Nothing in cache
	return b.EndpointFor(ctx, uuid.NewRandom())
}

func (b *BTrDB) resyncMash() {
	b.epmu.RLock()
	for _, ep := range b.epcache {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		mash, err := ep.Info(ctx)
		cancel()
		if err == nil {
			//TODO does this require a mutex?
			b.activeMash.Store(mash)
			b.epmu.RUnlock()
			return
		}
	}
	b.epmu.RUnlock()
	//TODO accessing nonexistent map key gives nil right?
	cm := b.activeMash.Load().(*MASH)
	for _, mbr := range cm.Members {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ep, err := b.EndpointForHash(ctx, mbr.Hash)
		cancel()
		if err != nil {
			continue
		}
		mash, err := ep.Info(ctx)
		if err == nil {
			b.activeMash.Store(mash)
			return
		}
	}
	panic("No endpoints reachable!")
}

//This returns true if you should redo your operation (and get new ep)
//and false if you should return the last value/error you got
func (b *BTrDB) testEpError(ep *Endpoint, err error) bool {
	ce := ToCodedError(err)
	if ce.Code == 405 {
		b.resyncMash()
		return true
	}
	return false
}

//This should invalidate the endpoint if some kind of error occurs.
//Because some values may have already been delivered, async functions using
//snoopEpErr will not be able to mask cluster errors from the user
func (b *BTrDB) snoopEpErr(ep *Endpoint, err chan error) chan error {
	rv := make(chan error, 2)
	go func() {
		for e := range err {
			//if e is special invalidate ep
			rv <- e
		}
		close(rv)
	}()
	return rv
}
