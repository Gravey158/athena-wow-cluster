package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/walkline/ToCloud9/apps/worlddbserver"
	"github.com/walkline/ToCloud9/apps/worlddbserver/repo"
	"github.com/walkline/ToCloud9/gen/worlddb/pb"
)

// WorldDBServer implements pb.WorldDBServiceServer. The Phase-1 design holds
// the whole creature_template table in process memory; reads are served from
// that snapshot, not from MySQL. The snapshot loads at boot and refreshes
// on a configurable interval (default: never -- worldservers re-pull on
// invalidation events, which Phase 3 will broadcast via NATS).
type WorldDBServer struct {
	pb.UnimplementedWorldDBServiceServer

	creatureTemplates struct {
		mu     sync.RWMutex
		byID   map[uint32]*repo.CreatureTemplate
		loaded bool
	}
}

func NewWorldDBServer() *WorldDBServer {
	s := &WorldDBServer{}
	s.creatureTemplates.byID = map[uint32]*repo.CreatureTemplate{}
	return s
}

// LoadCreatureTemplates populates the in-memory store from the given repo.
func (s *WorldDBServer) LoadCreatureTemplates(ctx context.Context, r repo.CreatureTemplateRepo) error {
	defer func(t time.Time) {
		log.Info().Dur("took", time.Since(t)).Msg("creature_template loaded into memory")
	}(time.Now())

	count, err := r.Count(ctx)
	if err != nil {
		return fmt.Errorf("count creature_template: %w", err)
	}

	fresh := make(map[uint32]*repo.CreatureTemplate, count)
	err = r.LoadAll(ctx, func(c *repo.CreatureTemplate) error {
		fresh[c.Entry] = c
		return nil
	})
	if err != nil {
		return fmt.Errorf("load creature_template rows: %w", err)
	}

	s.creatureTemplates.mu.Lock()
	s.creatureTemplates.byID = fresh
	s.creatureTemplates.loaded = true
	s.creatureTemplates.mu.Unlock()

	log.Info().Int("rows", len(fresh)).Msg("creature_template store ready")
	return nil
}

// GetAllCreatureTemplates streams every row exactly once.
func (s *WorldDBServer) GetAllCreatureTemplates(_ *pb.GetAllCreatureTemplatesRequest, stream pb.WorldDBService_GetAllCreatureTemplatesServer) error {
	s.creatureTemplates.mu.RLock()
	if !s.creatureTemplates.loaded {
		s.creatureTemplates.mu.RUnlock()
		return fmt.Errorf("creature_template not yet loaded")
	}
	snapshot := make([]*repo.CreatureTemplate, 0, len(s.creatureTemplates.byID))
	for _, c := range s.creatureTemplates.byID {
		snapshot = append(snapshot, c)
	}
	s.creatureTemplates.mu.RUnlock()

	ctx := stream.Context()
	for _, c := range snapshot {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stream.Send(creatureTemplateToProto(c)); err != nil {
			return err
		}
	}
	log.Debug().Int("rows", len(snapshot)).Msg("streamed creature_template snapshot")
	return nil
}

func creatureTemplateToProto(c *repo.CreatureTemplate) *pb.CreatureTemplate {
	return &pb.CreatureTemplate{
		Entry:                c.Entry,
		DifficultyEntry_1:    c.DifficultyEntry1,
		DifficultyEntry_2:    c.DifficultyEntry2,
		DifficultyEntry_3:    c.DifficultyEntry3,
		KillCredit_1:         c.KillCredit1,
		KillCredit_2:         c.KillCredit2,
		Name:                 c.Name,
		SubName:              c.SubName.String,
		IconName:             c.IconName.String,
		GossipMenuId:         c.GossipMenuID,
		MinLevel:             c.MinLevel,
		MaxLevel:             c.MaxLevel,
		Exp:                  c.Exp,
		Faction:              c.Faction,
		NpcFlag:              c.NpcFlag,
		SpeedWalk:            c.SpeedWalk,
		SpeedRun:             c.SpeedRun,
		SpeedSwim:            c.SpeedSwim,
		SpeedFlight:          c.SpeedFlight,
		DetectionRange:       c.DetectionRange,
		Rank:                 c.Rank,
		DmgSchool:            c.DmgSchool,
		DamageModifier:       c.DamageModifier,
		BaseAttackTime:       c.BaseAttackTime,
		RangeAttackTime:      c.RangeAttackTime,
		BaseVariance:         c.BaseVariance,
		RangeVariance:        c.RangeVariance,
		UnitClass:            c.UnitClass,
		UnitFlags:            c.UnitFlags,
		UnitFlags_2:          c.UnitFlags2,
		DynamicFlags:         c.DynamicFlags,
		Family:               c.Family,
		Type:                 c.Type,
		TypeFlags:            c.TypeFlags,
		Lootid:               c.LootID,
		PickpocketLoot:       c.PickpocketLoot,
		SkinningLoot:         c.SkinningLoot,
		PetSpellDataId:       c.PetSpellDataID,
		VehicleId:            c.VehicleID,
		MinGold:              c.MinGold,
		MaxGold:              c.MaxGold,
		AiName:               c.AIName,
		MovementType:         c.MovementType,
		HoverHeight:          c.HoverHeight,
		HealthModifier:       c.HealthModifier,
		ManaModifier:         c.ManaModifier,
		ArmorModifier:        c.ArmorModifier,
		ExperienceModifier:   c.ExperienceModifier,
		RacialLeader:         c.RacialLeader,
		MovementId:           c.MovementID,
		RegenHealth:          c.RegenHealth,
		CreatureImmunitiesId: c.CreatureImmunitiesID,
		FlagsExtra:           c.FlagsExtra,
		ScriptName:           c.ScriptName,
	}
}

// Ver is exposed for log/Ver checks.
var _ = worlddbserver.Ver
