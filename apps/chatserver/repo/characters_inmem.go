package repo

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// B33/B34/B36 fix: previously had separate guidMu + nameMu RWMutexes covering
// the two indexes. Problems:
//   - AddCharacter unlocked guidMu before locking nameMu => readers could see
//     a character existing in charsByGUID but not yet in charsByName.
//   - RemoveCharacter same temporal-inconsistency window.
//   - RemoveCharactersWithRealm only held nameMu but mutated charsByGUID too
//     (literal data race; the author left a "TODO: need to completely rewrite
//     this" comment marker).
// Replaced with a single RWMutex covering both indexes -- both maps mutate +
// query atomically. Simpler, correct, no measurable perf delta at our scale.
type charactersInMemRepo struct {
	mu          sync.RWMutex
	charsByGUID map[string]*Character
	charsByName map[string]*Character
}

func NewCharactersInMemRepo() CharactersRepo {
	return &charactersInMemRepo{
		charsByGUID: map[string]*Character{},
		charsByName: map[string]*Character{},
	}
}

func (c *charactersInMemRepo) AddCharacter(ctx context.Context, character *Character) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.charsByGUID[c.mapKeyForRealmAndGuid(character.RealmID, character.GUID)] = character
	c.charsByName[c.mapKeyForRealmAndName(character.RealmID, character.Name)] = character
	return nil
}

func (c *charactersInMemRepo) RemoveCharacter(ctx context.Context, realmID uint32, characterGUID uint64) error {
	guidKey := c.mapKeyForRealmAndGuid(realmID, characterGUID)
	c.mu.Lock()
	defer c.mu.Unlock()
	char := c.charsByGUID[guidKey]
	delete(c.charsByGUID, guidKey)
	if char != nil {
		delete(c.charsByName, c.mapKeyForRealmAndName(realmID, char.Name))
	}
	return nil
}

func (c *charactersInMemRepo) CharacterByRealmAndGUID(ctx context.Context, realmID uint32, characterGUID uint64) (*Character, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.charsByGUID[c.mapKeyForRealmAndGuid(realmID, characterGUID)], nil
}

func (c *charactersInMemRepo) CharacterByRealmAndName(ctx context.Context, realmID uint32, name string) (*Character, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.charsByName[c.mapKeyForRealmAndName(realmID, name)], nil
}

func (c *charactersInMemRepo) RemoveCharactersWithRealm(ctx context.Context, realmID uint32) error {
	prefix := fmt.Sprintf("%d:", realmID)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Iterate one index, collect the matching keys for the other, then
	// delete from both. Single lock => atomic for any concurrent reader.
	guidKeysToDelete := []string{}
	for k, char := range c.charsByName {
		if strings.HasPrefix(k, prefix) {
			guidKeysToDelete = append(guidKeysToDelete, c.mapKeyForRealmAndGuid(realmID, char.GUID))
			delete(c.charsByName, k) // safe: Go allows delete during range
		}
	}
	for _, k := range guidKeysToDelete {
		delete(c.charsByGUID, k)
	}
	return nil
}

func (c *charactersInMemRepo) mapKeyForRealmAndGuid(realm uint32, guid uint64) string {
	return fmt.Sprintf("%d:%d", realm, guid)
}

func (c *charactersInMemRepo) mapKeyForRealmAndName(realm uint32, name string) string {
	return fmt.Sprintf("%d:%s", realm, strings.ToLower(name))
}
