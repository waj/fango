package native

import (
	"reflect"
	"testing"

	"github.com/waj/fango/runtime/fangort"
)

func TestEventQueuePolicies(t *testing.T) {
	for _, test := range []struct {
		name string
		code int64
		want []int64
	}{
		{"fail", 0, []int64{-3}},
		{"drop oldest", 1, []int64{1, 2, -1}},
		{"drop newest", 2, []int64{0, 1, -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := EventNew(0, 0, 2, test.code)
			s := state.(*eventState)
			for i := int64(0); i < 3; i++ {
				s.push(i)
			}
			got := make([]int64, len(test.want))
			for i := range got {
				got[i] = TakeEvent(state)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("events = %v, want %v", got, test.want)
			}
			EventClose(state)
		})
	}
}

func TestEventUnregisterRacesNotification(t *testing.T) {
	for i := 0; i < 100; i++ {
		bridge := fangort.NewNativeEventBridge(1)
		state := EventNew(0, 1000, 2, 1)
		ticket := bridge.Reserve()
		Arm(state, ticket)
		Start(state, bridge)
		if got := bridge.Wait(); got != ticket {
			t.Fatalf("notification = %d, want ticket %d", got, ticket)
		}
		closed := make(chan struct{})
		go func() {
			EventClose(state)
			close(closed)
		}()
		Disarm(state, ticket)
		bridge.Release(ticket)
		<-closed
		if !state.(*eventState).closed || state.(*eventState).queue != nil {
			t.Fatal("subscription retained state after close")
		}
		bridge.Close()
	}
}
