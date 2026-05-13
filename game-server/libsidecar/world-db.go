package main

/*
#include <stdlib.h>
#include "world-db.h"
*/
import "C"

import (
	"context"
	"io"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/walkline/ToCloud9/game-server/libsidecar/config"
	"github.com/walkline/ToCloud9/gen/worlddb/pb"
)

// worldDBClient is set up once during initLib via SetupWorldDBConnection.
// nil means the worlddbserver feature is disabled -- TC9LoadAllCreatureTemplates
// returns -1 and the AC side falls back to MySQL.
var worldDBClient pb.WorldDBServiceClient

// SetupWorldDBConnection dials the worlddbserver microservice if its
// address is configured. Empty config means "disabled" (default) -- we
// only enable the gRPC path when WORLD_DB_SERVICE_ADDRESS is explicitly
// set in the environment. This makes the rollout opt-in per gameserver
// pod and lets us A/B test the path during ADR-007 Phase 2.
//
// Returns the connection so the caller can close it on shutdown. Returns
// nil if the feature is disabled.
func SetupWorldDBConnection(cfg *config.Config) *grpc.ClientConn {
	if cfg.WorldDBServiceAddress == "" {
		log.Info().Msg("worlddbserver disabled (WORLD_DB_SERVICE_ADDRESS not set); ObjectMgr will load from MySQL")
		return nil
	}

	conn, err := grpc.Dial(cfg.WorldDBServiceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatal().Err(err).Str("address", cfg.WorldDBServiceAddress).Msg("can't connect to worlddbserver")
	}
	worldDBClient = pb.NewWorldDBServiceClient(conn)
	log.Info().Str("address", cfg.WorldDBServiceAddress).Msg("worlddbserver connection established")
	return conn
}

// TC9LoadAllCreatureTemplates streams every row from the worlddbserver and
// dispatches each through the previously-registered C handler.
//
// Returns:
//
//	>=0 : number of rows successfully dispatched (0 if disabled)
//	-1  : feature disabled, RPC error, or worldserver not reachable; caller
//	      falls back to MySQL
//	<-1 : per-row handler aborted; caller treats as fatal
//
// The C++ side is responsible for copying string fields before the handler
// returns -- libsidecar frees them immediately after.
//
//export TC9LoadAllCreatureTemplates
func TC9LoadAllCreatureTemplates() C.int {
	if worldDBClient == nil {
		return -1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream, err := worldDBClient.GetAllCreatureTemplates(ctx, &pb.GetAllCreatureTemplatesRequest{Api: libVer})
	if err != nil {
		log.Err(err).Msg("GetAllCreatureTemplates RPC failed; AC will fall back to MySQL")
		return -1
	}

	count := 0
	for {
		row, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Err(err).Int("rows_received", count).Msg("creature_template stream errored mid-flight")
			return -1
		}

		// Allocate C strings just-in-time for this row; they live only
		// until the handler returns. The handler must copy them.
		cName := C.CString(row.Name)
		cSub := C.CString(row.SubName)
		cIcon := C.CString(row.IconName)
		cAI := C.CString(row.AiName)
		cScript := C.CString(row.ScriptName)

		var cRow C.CreatureTemplateRow
		cRow.entry = C.uint32_t(row.Entry)
		cRow.difficulty_entry_1 = C.uint32_t(row.DifficultyEntry_1)
		cRow.difficulty_entry_2 = C.uint32_t(row.DifficultyEntry_2)
		cRow.difficulty_entry_3 = C.uint32_t(row.DifficultyEntry_3)
		cRow.kill_credit_1 = C.uint32_t(row.KillCredit_1)
		cRow.kill_credit_2 = C.uint32_t(row.KillCredit_2)
		cRow.name = cName
		cRow.sub_name = cSub
		cRow.icon_name = cIcon
		cRow.gossip_menu_id = C.uint32_t(row.GossipMenuId)
		cRow.min_level = C.uint32_t(row.MinLevel)
		cRow.max_level = C.uint32_t(row.MaxLevel)
		cRow.exp = C.int32_t(row.Exp)
		cRow.faction = C.uint32_t(row.Faction)
		cRow.npc_flag = C.uint32_t(row.NpcFlag)
		cRow.speed_walk = C.float(row.SpeedWalk)
		cRow.speed_run = C.float(row.SpeedRun)
		cRow.speed_swim = C.float(row.SpeedSwim)
		cRow.speed_flight = C.float(row.SpeedFlight)
		cRow.detection_range = C.float(row.DetectionRange)
		cRow.rank = C.uint32_t(row.Rank)
		cRow.dmg_school = C.int32_t(row.DmgSchool)
		cRow.damage_modifier = C.float(row.DamageModifier)
		cRow.base_attack_time = C.uint32_t(row.BaseAttackTime)
		cRow.range_attack_time = C.uint32_t(row.RangeAttackTime)
		cRow.base_variance = C.float(row.BaseVariance)
		cRow.range_variance = C.float(row.RangeVariance)
		cRow.unit_class = C.uint32_t(row.UnitClass)
		cRow.unit_flags = C.uint32_t(row.UnitFlags)
		cRow.unit_flags_2 = C.uint32_t(row.UnitFlags_2)
		cRow.dynamic_flags = C.uint32_t(row.DynamicFlags)
		cRow.family = C.int32_t(row.Family)
		cRow._type = C.uint32_t(row.Type)
		cRow.type_flags = C.uint32_t(row.TypeFlags)
		cRow.lootid = C.uint32_t(row.Lootid)
		cRow.pickpocket_loot = C.uint32_t(row.PickpocketLoot)
		cRow.skinning_loot = C.uint32_t(row.SkinningLoot)
		cRow.pet_spell_data_id = C.uint32_t(row.PetSpellDataId)
		cRow.vehicle_id = C.uint32_t(row.VehicleId)
		cRow.min_gold = C.uint32_t(row.MinGold)
		cRow.max_gold = C.uint32_t(row.MaxGold)
		cRow.ai_name = cAI
		cRow.movement_type = C.uint32_t(row.MovementType)
		cRow.hover_height = C.float(row.HoverHeight)
		cRow.health_modifier = C.float(row.HealthModifier)
		cRow.mana_modifier = C.float(row.ManaModifier)
		cRow.armor_modifier = C.float(row.ArmorModifier)
		cRow.experience_modifier = C.float(row.ExperienceModifier)
		cRow.racial_leader = C.uint32_t(row.RacialLeader)
		cRow.movement_id = C.uint32_t(row.MovementId)
		cRow.regen_health = C.uint32_t(row.RegenHealth)
		cRow.creature_immunities_id = C.int32_t(row.CreatureImmunitiesId)
		cRow.flags_extra = C.uint32_t(row.FlagsExtra)
		cRow.script_name = cScript

		rc := C.TC9InvokeCreatureTemplateRowHandler(&cRow)

		C.free(unsafe.Pointer(cName))
		C.free(unsafe.Pointer(cSub))
		C.free(unsafe.Pointer(cIcon))
		C.free(unsafe.Pointer(cAI))
		C.free(unsafe.Pointer(cScript))

		if rc != 0 {
			log.Error().Int("rc", int(rc)).Uint32("entry", row.Entry).Msg("creature_template row handler aborted")
			return C.int(-int(rc) - 1)
		}
		count++
	}

	log.Info().Int("rows", count).Msg("creature_template loaded from worlddbserver into AC ObjectMgr")
	return C.int(count)
}
