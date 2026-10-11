package compilecache

// EmissionStore is the opaque byte-storage seam for per-owner generated Go.
// The backend package owns the artifact schema and its validation.
type EmissionStore struct {
	store *slotStore
	debug bool
}

func NewEmissionStore(entry string) *EmissionStore { return &EmissionStore{store: newStore(entry)} }

func NewDebugEmissionStore(entry string) *EmissionStore {
	return &EmissionStore{store: newStore(entry), debug: true}
}
func (s *EmissionStore) kind() string {
	if s.debug {
		return "emitted-debug-v1"
	}
	return emittedKind
}

func (s *EmissionStore) Load(slot string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	return s.store.load(s.kind(), slot)
}

func (s *EmissionStore) Store(slot string, data []byte) {
	if s == nil {
		return
	}
	s.store.store(s.kind(), slot, data)
}
