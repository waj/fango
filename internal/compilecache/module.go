package compilecache

// ModuleStore is the opaque byte-storage seam for checked module objects.
// The check package owns the object schema and its validation.
type ModuleStore struct {
	store *slotStore
}

func NewModuleStore(entry string) *ModuleStore { return &ModuleStore{store: newStore(entry)} }

func (s *ModuleStore) LoadObject(slot string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	return s.store.load(checkedKind, slot)
}

func (s *ModuleStore) StoreObject(slot string, data []byte) {
	if s == nil {
		return
	}
	s.store.store(checkedKind, slot, data)
}
