package ethereum

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/umbracle/minimal/blockchain"
)

func testPeerAncestor(t *testing.T, h0 []*types.Header, h1 []*types.Header, ancestor *types.Header) {
	b0 := blockchain.NewTestBlockchain(t, h0)
	b1 := blockchain.NewTestBlockchain(t, h1)

	syncer, err := NewBackend(nil, b0)
	if err != nil {
		t.Fatal(err)
	}

	eth0, _ := ethPipe(b0, b1)

	height, err := eth0.fetchHeight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := syncer.FindCommonAncestor(eth0, height)
	if err != nil {
		t.Fatal(err)
	}
	if ancestor == nil && h != nil {
		t.Fatal("expected nothing but header has content")
	}

	fmt.Println(h.Hash().String())
	fmt.Println(ancestor.Hash().String())

	if h.Hash() != ancestor.Hash() {
		t.Fatal("hash dont match")
	}
}

func TestPeerFindCommonAncestor(t *testing.T) {
	t.Run("Server with shorter chain", func(t *testing.T) {
		headers := blockchain.NewTestHeaderChain(1000)
		testPeerAncestor(t, headers[0:5], headers, headers[4])
	})

	t.Run("Server with shorter chain odd", func(t *testing.T) {
		headers := blockchain.NewTestHeaderChain(999)
		testPeerAncestor(t, headers[0:7], headers, headers[6])
	})

	t.Run("Server with longer chain", func(t *testing.T) {
		headers := blockchain.NewTestHeaderChain(1000)
		testPeerAncestor(t, headers, headers[0:5], headers[4])
	})

	t.Run("Same chain", func(t *testing.T) {
		headers := blockchain.NewTestHeaderChain(100)
		testPeerAncestor(t, headers, headers, headers[len(headers)-1])
	})

	t.Run("No matches", func(t *testing.T) {
		h0 := blockchain.NewTestHeaderChain(100)
		h1 := blockchain.NewTestHeaderChainWithSeed(nil, 100, 10)
		testPeerAncestor(t, h0, h1, nil)
	})

	t.Run("Ancestor is genesis", func(t *testing.T) {
		genesis := blockchain.NewTestHeaderChain(1)

		h0 := blockchain.NewTestHeaderFromChain(genesis, 10)
		h1 := blockchain.NewTestHeaderFromChainWithSeed(genesis, 10, 10)

		testPeerAncestor(t, h0, h1, genesis[0])
	})

	// TODO, ancestor with forked chain
}

func TestMaxConcurrentTasks(t *testing.T) {
	b0 := blockchain.NewTestBlockchain(t, blockchain.NewTestHeaderChain(1000))

	b, err := NewBackend(nil, b0)
	assert.NoError(t, err)

	peekCh := func(b *Backend) bool {
		workerCh := make(chan *worker)
		go func() {
			w := b.peek()
			workerCh <- w
		}()

		select {
		case <-workerCh:
			return true
		case <-time.After(10 * time.Millisecond):
			return false
		}
	}

	// Add enough peers to reach max concurrent tasks
	for _, p := range []string{"1", "2", "3", "4"} {
		b.addPeer(p, nil)
	}
	for i := 0; i < maxConcurrentTasks; i++ {
		b.peek()
	}

	// No peek if maxConcurrentTasks reached
	assert.False(t, peekCh(b))

	b, err = NewBackend(nil, b0)
	assert.NoError(t, err)

	// Add enough peers to reach max concurrent tasks
	for _, p := range []string{"1", "2"} {
		b.addPeer(p, nil)
	}
	for i := 0; i < maxOutstandingRequests*2; i++ {
		b.peek()
	}

	// No peek if all the peers are busy
	assert.False(t, peekCh(b))
}

func TestPeerDequeueIncreaseOutstandingCount(t *testing.T) {
	// Every new peek should increase the outstanding request count

	b0 := blockchain.NewTestBlockchain(t, blockchain.NewTestHeaderChain(1000))

	b, err := NewBackend(nil, b0)
	assert.NoError(t, err)

	peers := map[string]int{}
	for _, p := range []string{"1", "2"} {
		b.addPeer(p, nil)
		peers[p] = 1
	}

	for i := 0; i < maxOutstandingRequests*2; i++ {
		w := b.peek()
		assert.Equal(t, peers[w.id], w.outstanding)
		peers[w.id]++
	}
}

func ethPipe(b0, b1 *blockchain.Blockchain) (*Ethereum, *Ethereum) {
	h0, _ := b0.Header()
	st0 := &Status{
		ProtocolVersion: 63,
		NetworkID:       1,
		TD:              big.NewInt(1),
		CurrentBlock:    h0.Hash(),
		GenesisBlock:    b0.Genesis().Hash(),
	}

	h1, _ := b1.Header()
	st1 := &Status{
		ProtocolVersion: 63,
		NetworkID:       1,
		TD:              big.NewInt(1),
		CurrentBlock:    h1.Hash(),
		GenesisBlock:    b1.Genesis().Hash(),
	}

	conn0, conn1 := net.Pipe()
	eth0 := NewEthereumProtocol("", conn0, b0)
	eth1 := NewEthereumProtocol("", conn1, b1)

	err := make(chan error)
	go func() {
		err <- eth0.Init(st0)
	}()
	go func() {
		err <- eth1.Init(st1)
	}()

	if err := <-err; err != nil {
		panic(err)
	}
	if err := <-err; err != nil {
		panic(err)
	}
	return eth0, eth1
}

func testEthereum(conn net.Conn, b *blockchain.Blockchain) *Ethereum {
	h, _ := b.Header()
	st := &status
	st.CurrentBlock = h.Hash()
	st.GenesisBlock = b.Genesis().Hash()

	eth := NewEthereumProtocol("", conn, b)
	if err := eth.Init(st); err != nil {
		panic(err)
	}
	return eth
}

func TestBackendBroadcastBlock(t *testing.T) {
	headers := blockchain.NewTestHeaderChain(1000)

	// b0 with only the genesis
	b0 := blockchain.NewTestBlockchain(t, headers)

	b, err := NewBackend(nil, b0)
	if err != nil {
		panic(err)
	}

	c0, c1 := net.Pipe()

	var eth *Ethereum
	go func() {
		eth = testEthereum(c1, b0)
	}()
	if _, err := b.Add(c0, "1"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1 * time.Second)

	fmt.Println("-- eth --")
	fmt.Println(eth)

	/*
		watch := eth.Watch()

		req := b0.GetBlockByNumber(big.NewInt(100), true)
		b.broadcast(req)

		recv := <-watch
		if recv.Block.Number().Uint64() != req.Number().Uint64() {
			t.Fatal("bad")
		}
	*/
}

func TestBackendNotify(t *testing.T) {

	h0 := blockchain.NewTestHeaderChain(10)
	h1 := blockchain.NewTestHeaderFromChain(h0[1:5], 10)

	b1 := blockchain.HeadersToBlocks(h1)

	b := blockchain.NewTestBlockchain(t, h0)
	fmt.Println(b)

	if err := b.WriteBlocks(b1); err != nil {
		panic(err)
	}

	fmt.Println("-- forks --")
	fmt.Println(b.GetForks())
}