package native

type batch struct {
	window  int
	pending []byte
	total   int64
}

func NewBatch(window int64) any {
	return &batch{window: int(window)}
}

// BatchPush appends data and answers every pending byte once the window is
// reached, or nothing. The answer is fresh storage the batch never touches
// again.
func BatchPush(value any, data []byte) []byte {
	b := value.(*batch)
	b.total += int64(len(data))
	if len(b.pending) == 0 && len(data) >= b.window {
		return data
	}
	b.pending = append(b.pending, data...)
	if len(b.pending) < b.window {
		return nil
	}
	return b.take()
}

func BatchTake(value any) []byte {
	return value.(*batch).take()
}

func BatchTotal(value any) int64 {
	return value.(*batch).total
}

func (b *batch) take() []byte {
	result := b.pending
	b.pending = nil
	return result
}
