package compilecache

import (
	"fmt"

	"github.com/waj/fango/internal/debugger"
)

// ModuleStore is the opaque byte-storage seam for checked module objects.
// The check package owns the object schema and its validation.
type ModuleStore struct {
	store *slotStore
	debug bool
}

func NewModuleStore(entry string) *ModuleStore { return &ModuleStore{store: newStore(entry)} }

func NewDebugModuleStore(entry string) *ModuleStore {
	return &ModuleStore{store: newStore(entry), debug: true}
}
func (s *ModuleStore) kind() string {
	if s.debug {
		return fmt.Sprintf("checked-debug-v%d", debugger.Version)
	}
	return checkedKind
}

func (s *ModuleStore) LoadObject(slot string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	return s.store.load(s.kind(), slot)
}

func (s *ModuleStore) StoreObject(slot string, data []byte) {
	if s == nil {
		return
	}
	s.store.store(s.kind(), slot, data)
}
