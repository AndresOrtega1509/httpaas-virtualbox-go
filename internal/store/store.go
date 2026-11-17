package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"httpaas/internal/model"
)

type Store struct {
	mu   sync.RWMutex
	path string
	data map[string]model.Instance
}

func New(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]model.Instance{}}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, s.saveLocked()
		}
		return nil, err
	}
	if len(b) > 0 {
		var items []model.Instance
		if err := json.Unmarshal(b, &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			s.data[it.Name] = it
		}
	}
	return s, nil
}

func (s *Store) List() []model.Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]model.Instance, 0, len(s.data))
	for _, it := range s.data {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items
}

func (s *Store) Get(name string) (model.Instance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.data[name]
	return it, ok
}

func (s *Store) Upsert(it model.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it.CreatedAt.IsZero() {
		if old, ok := s.data[it.Name]; ok {
			it.CreatedAt = old.CreatedAt
		} else {
			it.CreatedAt = time.Now()
		}
	}
	it.UpdatedAt = time.Now()
	s.data[it.Name] = it
	return s.saveLocked()
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, name)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	items := make([]model.Instance, 0, len(s.data))
	for _, it := range s.data {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
