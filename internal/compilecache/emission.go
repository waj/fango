package compilecache

// EmissionStore is the opaque byte-storage seam for per-owner generated Go.
// The backend package owns the artifact schema and its validation.
type EmissionStore struct {
	store *slotStore
}

func NewEmissionStore(entry string) *EmissionStore { return &EmissionStore{store: newStore(entry)} }

func (s *EmissionStore) Load(slot string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	return s.store.load(emittedKind, slot)
}

func (s *EmissionStore) Store(slot string, data []byte) {
	if s == nil {
		return
	}
	s.store.store(emittedKind, slot, data)
}
