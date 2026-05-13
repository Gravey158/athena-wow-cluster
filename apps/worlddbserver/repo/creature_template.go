package repo

import (
	"context"
	"database/sql"
)

// CreatureTemplate mirrors the subset of acore_world.creature_template columns
// the worldserver's ObjectMgr::LoadCreatureTemplates reads. Schema as seen in
// the walkline AC fork @ f3d7356: no modelid1..4 (in join table), no
// resistance/spell columns, no trainer_*, single CreatureImmunitiesId field
// instead of bitmasks, speed_swim/speed_flight/detection_range present.
type CreatureTemplate struct {
	Entry               uint32
	DifficultyEntry1    uint32
	DifficultyEntry2    uint32
	DifficultyEntry3    uint32
	KillCredit1         uint32
	KillCredit2         uint32
	Name                string
	SubName             sql.NullString
	IconName            sql.NullString
	GossipMenuID        uint32
	MinLevel            uint32
	MaxLevel            uint32
	Exp                 int32
	Faction             uint32
	NpcFlag             uint32
	SpeedWalk           float32
	SpeedRun            float32
	SpeedSwim           float32
	SpeedFlight         float32
	DetectionRange      float32
	Rank                uint32
	DmgSchool           int32
	DamageModifier      float32
	BaseAttackTime      uint32
	RangeAttackTime     uint32
	BaseVariance        float32
	RangeVariance       float32
	UnitClass           uint32
	UnitFlags           uint32
	UnitFlags2          uint32
	DynamicFlags        uint32
	Family              int32
	Type                uint32
	TypeFlags           uint32
	LootID              uint32
	PickpocketLoot      uint32
	SkinningLoot        uint32
	PetSpellDataID      uint32
	VehicleID           uint32
	MinGold             uint32
	MaxGold             uint32
	AIName              string
	MovementType        uint32
	HoverHeight         float32
	HealthModifier      float32
	ManaModifier        float32
	ArmorModifier       float32
	ExperienceModifier  float32
	RacialLeader        uint32
	MovementID          uint32
	RegenHealth         uint32
	CreatureImmunitiesID int32
	FlagsExtra          uint32
	ScriptName          string
}

// CreatureTemplateRepo reads creature_template rows. The implementation
// loads the entire table in one pass; AC's ObjectMgr does the same on
// boot today, so the memory shape is unchanged -- we just centralized
// where it lives.
type CreatureTemplateRepo interface {
	// LoadAll streams every creature_template row through f. Returning an
	// error from f aborts the load.
	LoadAll(ctx context.Context, f func(*CreatureTemplate) error) error

	// Count returns the row count of creature_template.
	Count(ctx context.Context) (int, error)
}

// NewCreatureTemplateMySQLRepo returns a MySQL-backed CreatureTemplateRepo.
func NewCreatureTemplateMySQLRepo(db *sql.DB) CreatureTemplateRepo {
	return &creatureTemplateMySQLRepo{db: db}
}

type creatureTemplateMySQLRepo struct {
	db *sql.DB
}

func (r *creatureTemplateMySQLRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM creature_template").Scan(&n)
	return n, err
}

func (r *creatureTemplateMySQLRepo) LoadAll(ctx context.Context, f func(*CreatureTemplate) error) error {
	// Column list matches the struct field order. `rank` is a reserved word
	// in MySQL 8 so it gets backticked; the rest are bare names.
	const query = "SELECT " +
		"entry, " +
		"difficulty_entry_1, difficulty_entry_2, difficulty_entry_3, " +
		"KillCredit1, KillCredit2, " +
		"name, subname, IconName, " +
		"gossip_menu_id, " +
		"minlevel, maxlevel, " +
		"exp, " +
		"faction, npcflag, " +
		"speed_walk, speed_run, speed_swim, speed_flight, detection_range, " +
		"`rank`, " +
		"dmgschool, DamageModifier, " +
		"BaseAttackTime, RangeAttackTime, " +
		"BaseVariance, RangeVariance, " +
		"unit_class, unit_flags, unit_flags2, " +
		"dynamicflags, " +
		"family, type, type_flags, " +
		"lootid, pickpocketloot, skinloot, " +
		"PetSpellDataId, VehicleId, " +
		"mingold, maxgold, " +
		"AIName, MovementType, HoverHeight, " +
		"HealthModifier, ManaModifier, ArmorModifier, ExperienceModifier, " +
		"RacialLeader, movementId, RegenHealth, " +
		"CreatureImmunitiesId, flags_extra, ScriptName " +
		"FROM creature_template"

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var c CreatureTemplate
		err := rows.Scan(
			&c.Entry,
			&c.DifficultyEntry1, &c.DifficultyEntry2, &c.DifficultyEntry3,
			&c.KillCredit1, &c.KillCredit2,
			&c.Name, &c.SubName, &c.IconName,
			&c.GossipMenuID,
			&c.MinLevel, &c.MaxLevel,
			&c.Exp,
			&c.Faction, &c.NpcFlag,
			&c.SpeedWalk, &c.SpeedRun, &c.SpeedSwim, &c.SpeedFlight, &c.DetectionRange,
			&c.Rank,
			&c.DmgSchool, &c.DamageModifier,
			&c.BaseAttackTime, &c.RangeAttackTime,
			&c.BaseVariance, &c.RangeVariance,
			&c.UnitClass, &c.UnitFlags, &c.UnitFlags2,
			&c.DynamicFlags,
			&c.Family, &c.Type, &c.TypeFlags,
			&c.LootID, &c.PickpocketLoot, &c.SkinningLoot,
			&c.PetSpellDataID, &c.VehicleID,
			&c.MinGold, &c.MaxGold,
			&c.AIName, &c.MovementType, &c.HoverHeight,
			&c.HealthModifier, &c.ManaModifier, &c.ArmorModifier, &c.ExperienceModifier,
			&c.RacialLeader, &c.MovementID, &c.RegenHealth,
			&c.CreatureImmunitiesID, &c.FlagsExtra, &c.ScriptName,
		)
		if err != nil {
			return err
		}
		if err := f(&c); err != nil {
			return err
		}
	}
	return rows.Err()
}
