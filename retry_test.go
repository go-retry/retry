// Copyright 2011, 2012, 2013 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package retry_test // import "gopkg.in/retry.v1"

import (
	"testing"
	"time"

	"github.com/rogpeppe/clock/testclock"
	qt "github.com/frankban/quicktest"

	"gopkg.in/retry.v1"
)

func TestAttemptTiming(t *testing.T) {
	c := qt.New(t)
	testAttempt := retry.Regular{
		Total: 0.25e9,
		Delay: 0.1e9,
	}
	want := []time.Duration{0, 0.1e9, 0.2e9, 0.2e9}
	got := make([]time.Duration, 0, len(want)) // avoid allocation when testing timing
	t0 := time.Now()
	a := testAttempt.Start(nil)
	for a.Next() {
		got = append(got, time.Now().Sub(t0))
	}
	got = append(got, time.Now().Sub(t0))
	c.Assert(a.Stopped(), qt.Equals, false)
	c.Assert(got, qt.HasLen, len(want))
	const margin = 0.01e9
	for i, got := range want {
		lo := want[i] - margin
		hi := want[i] + margin
		if got < lo || got > hi {
			c.Errorf("attempt %d want %g got %g", i, want[i].Seconds(), got.Seconds())
		}
	}
}

func TestAttemptNextMore(t *testing.T) {
	c := qt.New(t)
	a := retry.Regular{}.Start(nil)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, false)

	a = retry.Regular{}.Start(nil)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.More(), qt.Equals, false)
	c.Assert(a.Next(), qt.Equals, false)

	a = retry.Regular{Total: 2e8}.Start(nil)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.More(), qt.Equals, true)
	time.Sleep(2e8)
	c.Assert(a.More(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, false)

	a = retry.Regular{Total: 1e8, Min: 2}.Start(nil)
	time.Sleep(1e8)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.More(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(a.More(), qt.Equals, false)
	c.Assert(a.Next(), qt.Equals, false)
}

func TestAttemptWithStop(t *testing.T) {
	c := qt.New(t)
	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	go func() {
		strategy := retry.Regular{
			Delay: 5 * time.Second,
			Total: 30 * time.Second,
		}
		a := retry.StartWithCancel(strategy, nil, stop)
		for a.Next() {
			c.Errorf("unexpected attempt")
		}
		c.Check(a.Stopped(), qt.Equals, true)
		close(done)
	}()
	assertReceive(c, done, "attempt loop abort")
}

func TestAttemptWithLaterStop(t *testing.T) {
	c := qt.New(t)
	clock := testclock.NewClock(time.Now())
	stop := make(chan struct{})
	done := make(chan struct{})
	progress := make(chan struct{}, 10)
	go func() {
		strategy := retry.Regular{
			Delay: 5 * time.Second,
			Total: 30 * time.Second,
		}
		a := retry.StartWithCancel(strategy, clock, stop)
		for a.Next() {
			progress <- struct{}{}
		}
		c.Check(a.Stopped(), qt.Equals, true)
		close(done)
	}()
	assertReceive(c, progress, "progress")
	clock.Advance(5 * time.Second)
	assertReceive(c, progress, "progress")
	clock.Advance(2 * time.Second)
	close(stop)
	assertReceive(c, done, "attempt loop abort")
	select {
	case <-progress:
		c.Fatalf("unxpected loop iteration after stop")
	default:
	}
}

func TestAttemptWithMockClock(t *testing.T) {
	c := qt.New(t)
	clock := testclock.NewClock(time.Now())
	strategy := retry.Regular{
		Delay: 5 * time.Second,
		Total: 30 * time.Second,
	}
	progress := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for a := strategy.Start(clock); a.Next(); {
			progress <- struct{}{}
		}
		close(done)
	}()
	assertReceive(c, progress, "progress first time")
	clock.Advance(5 * time.Second)
	assertReceive(c, progress, "progress second time")
	clock.Advance(5 * time.Second)
	assertReceive(c, progress, "progress third time")
	clock.Advance(30 * time.Second)
	assertReceive(c, progress, "progress fourth time")
	assertReceive(c, done, "loop finish")
}

type strategyTest struct {
	about      string
	strategy   retry.Strategy
	calls      []nextCall
	terminates bool
}

type nextCall struct {
	// t holds the time since the timer was started that
	// the Next call will be made.
	t time.Duration
	// delay holds the length of time that a call made at
	// time t is expected to sleep for.
	sleep time.Duration
}

var strategyTests = []strategyTest{{
	about: "regular retry (same params as TestAttemptTiming)",
	strategy: retry.Regular{
		Total: 0.25e9,
		Delay: 0.1e9,
	},
	calls: []nextCall{
		{0, 0},
		{0, 0.1e9},
		{0.1e9, 0.1e9},
		{0.2e9, 0},
	},
	terminates: true,
}, {
	about: "regular retry with calls at different times",
	strategy: retry.Regular{
		Total: 2.5e9,
		Delay: 1e9,
	},
	calls: []nextCall{
		{0.5e9, 0},
		{0.5e9, 0.5e9},
		{1.1e9, 0.9e9},
		{2.2e9, 0},
	},
	terminates: true,
}, {
	about: "regular retry with call after next deadline",
	strategy: retry.Regular{
		Total: 3.5e9,
		Delay: 1e9,
	},
	calls: []nextCall{
		{0.5e9, 0},
		// We call Next at well beyond the deadline,
		// so we get a zero delay, but subsequent events
		// resume pace.
		{2e9, 0},
		{2.1e9, 0.9e9},
		{3e9, 0},
	},
	terminates: true,
}, {
	about: "exponential retry",
	strategy: retry.Exponential{
		Initial: 1e9,
		Factor:  2,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 4e9},
		{7e9, 8e9},
	},
}, {
	about: "exponential retry with zero factor defaults to 2",
	strategy: retry.Exponential{
		Initial: 1e9,
	},
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 4e9},
		{7e9, 8e9},
	},
}, {
	about: "time-limited exponential retry",
	strategy: retry.LimitTime(5e9, retry.Exponential{
		Initial: 1e9,
		Factor:  2,
	}),
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 2e9},
		{3e9, 0},
	},
	terminates: true,
}, {
	about: "count-limited exponential retry",
	strategy: retry.LimitCount(2, retry.Exponential{
		Initial: 1e9,
		Factor:  2,
	}),
	calls: []nextCall{
		{0, 0},
		{0.1e9, 0.9e9},
		{1e9, 0},
	},
	terminates: true,
}}

func TestStrategies(t *testing.T) {
	c := qt.New(t)
	for i, test := range strategyTests {
		c.Logf("test %d: %s", i, test.about)
		testStrategy(c, test)
	}
}

func testStrategy(c *qt.C, test strategyTest) {
	t0 := time.Now()
	clk := &mockClock{
		now: t0,
	}
	a := retry.Start(test.strategy, clk)
	for i, call := range test.calls {
		c.Logf("call %d - %v", i, call.t)
		clk.now = t0.Add(call.t)
		ok := a.Next()
		expectTerminate := test.terminates && i == len(test.calls)-1
		c.Assert(ok, qt.Equals, !expectTerminate)
		if got, want := clk.now.Sub(t0), call.t+call.sleep; !closeTo(got, want) {
			c.Fatalf("incorrect time after Next; got %v want %v", got, want)
		}
		if ok {
			c.Assert(a.Count(), qt.Equals, i+1)
		}
	}
}

func TestExponentialWithJitter(t *testing.T) {
	c := qt.New(t)
	// We use a stochastic test because we don't want
	// to mock rand and have detailed dependence on
	// the exact way it's used. We run the strategy many
	// times and note the delays that we found; if the
	// jitter is working, the delays should be roughly equally
	// distributed and it shouldn't take long before all the
	// buckets are hit.
	const numBuckets = 8
	tries := []struct {
		max     time.Duration
		buckets [numBuckets]int
	}{{
		max: 1e9,
	}, {
		max: 2e9,
	}, {
		max: 4e9,
	}, {
		max: 5e9,
	}}
	strategy := retry.Exponential{
		Initial:  1e9,
		Factor:   2,
		MaxDelay: 5e9,
		Jitter:   true,
	}
	count := 0
	for i := 0; i < 10000 && count < len(tries)*numBuckets; i++ {
		clk := &mockClock{
			now: time.Now(),
		}
		t := clk.Now()
		a := retry.Start(strategy, clk)
		if !a.Next() {
			c.Fatalf("no first try")
		}
		if clk.Now().Sub(t) != 0 {
			c.Fatalf("first try was not immediate")
		}
		for try := 0; a.Next(); try++ {
			if try >= len(tries) {
				break
			}
			d := clk.Now().Sub(t)
			t = clk.Now()
			max := tries[try].max
			if d > max {
				c.Fatalf("try %d exceeded max %v; actual duration %v", try, tries[try].max, d)
			}
			slot := int(float64(d) / float64(max+1) * numBuckets)
			if slot >= numBuckets || slot < 0 {
				c.Fatalf("try %d slot %d out of range; d %v; max %v", try, slot, d, max)
			}
			buckets := &tries[try].buckets
			if buckets[slot] == 0 {
				count++
			}
			buckets[slot]++
		}
	}
	if count < len(tries)*numBuckets {
		c.Fatalf("distribution was not evenly spread; tries %#v", tries)
	}
}

func TestGapBetweenMoreAndNext(t *testing.T) {
	c := qt.New(t)
	t0 := time.Now().UTC()
	clk := &mockClock{
		now: t0,
	}
	a := (&retry.Regular{
		Min:   3,
		Delay: time.Second,
	}).Start(clk)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(clk.now, qt.Equals, t0)

	clk.now = clk.now.Add(500 * time.Millisecond)
	// Sanity check that the first iteration sleeps for half a second.
	c.Assert(a.More(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(clk.now.Sub(t0), qt.Equals, t0.Add(time.Second).Sub(t0))

	clk.now = clk.now.Add(500 * time.Millisecond)
	c.Assert(a.More(), qt.Equals, true)

	// Add a delay between calling More and Next.
	// Next should wait until the correct time anyway.
	clk.now = clk.now.Add(250 * time.Millisecond)
	c.Assert(a.More(), qt.Equals, true)
	c.Assert(a.Next(), qt.Equals, true)
	c.Assert(clk.now.Sub(t0), qt.Equals, t0.Add(2*time.Second).Sub(t0))
}

func TestOnlyOneHitOnZeroTotal(t *testing.T) {
	c := qt.New(t)
	t0 := time.Now().UTC()
	clk := &mockClock{
		now: t0,
	}
	a := (&retry.Regular{
		Total: 0,
		Delay: 0,
		Min:   0,
	}).Start(clk)
	// Even if the clock didn't advanced we want to have only one hit
	c.Check(a.Next(), qt.Equals, true)
	c.Check(a.More(), qt.Equals, false)
}

// closeTo reports whether d0 and d1 are close enough
// to one another to cater for inaccuracies of floating point arithmetic.
func closeTo(d0, d1 time.Duration) bool {
	const margin = 20 * time.Nanosecond
	diff := d1 - d0
	if diff < 0 {
		diff = -diff
	}
	return diff < margin
}

type mockClock struct {
	retry.Clock

	now   time.Time
	sleep func(d time.Duration)
}

func (c *mockClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time)
	close(ch)
	return ch
}

func (c *mockClock) Now() time.Time {
	return c.now
}

func assertReceive(c *qt.C, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	case <-time.After(time.Second):
		c.Fatalf("timed out waiting for %s", what)
	}
}
