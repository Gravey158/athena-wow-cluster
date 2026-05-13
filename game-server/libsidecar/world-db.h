#ifndef LIBSIDECAR_WORLD_DB_H
#define LIBSIDECAR_WORLD_DB_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// CreatureTemplateRow mirrors apps/worlddbserver/repo/CreatureTemplate.
// Field order + types match the wire form 1:1; string fields are owned by
// libsidecar for the duration of the handler call and become invalid as
// soon as the handler returns -- the C++ side must copy them before
// returning. Nullable MySQL columns (subname, IconName) come through as
// empty C strings when NULL on the SQL side.
typedef struct CreatureTemplateRow {
    uint32_t entry;
    uint32_t difficulty_entry_1;
    uint32_t difficulty_entry_2;
    uint32_t difficulty_entry_3;
    uint32_t kill_credit_1;
    uint32_t kill_credit_2;
    const char* name;
    const char* sub_name;
    const char* icon_name;
    uint32_t gossip_menu_id;
    uint32_t min_level;
    uint32_t max_level;
    int32_t exp;
    uint32_t faction;
    uint32_t npc_flag;
    float speed_walk;
    float speed_run;
    float speed_swim;
    float speed_flight;
    float detection_range;
    uint32_t rank;
    int32_t dmg_school;
    float damage_modifier;
    uint32_t base_attack_time;
    uint32_t range_attack_time;
    float base_variance;
    float range_variance;
    uint32_t unit_class;
    uint32_t unit_flags;
    uint32_t unit_flags_2;
    uint32_t dynamic_flags;
    int32_t family;
    uint32_t type;
    uint32_t type_flags;
    uint32_t lootid;
    uint32_t pickpocket_loot;
    uint32_t skinning_loot;
    uint32_t pet_spell_data_id;
    uint32_t vehicle_id;
    uint32_t min_gold;
    uint32_t max_gold;
    const char* ai_name;
    uint32_t movement_type;
    float hover_height;
    float health_modifier;
    float mana_modifier;
    float armor_modifier;
    float experience_modifier;
    uint32_t racial_leader;
    uint32_t movement_id;
    uint32_t regen_health;
    int32_t creature_immunities_id;
    uint32_t flags_extra;
    const char* script_name;
} CreatureTemplateRow;

// CreatureTemplateRowHandler is the per-row callback type. Returning non-zero
// from the handler aborts the load and propagates up as the return of
// TC9LoadAllCreatureTemplates.
typedef int (*CreatureTemplateRowHandler)(const CreatureTemplateRow*);

// TC9SetCreatureTemplateRowHandler registers the per-row handler. Must be
// called before TC9LoadAllCreatureTemplates. Setting NULL clears it.
extern void TC9SetCreatureTemplateRowHandler(CreatureTemplateRowHandler h);

// TC9InvokeCreatureTemplateRowHandler dispatches a single row to the registered
// handler. Returns the handler's return code, or 0 if no handler is set.
// Called from Go-side libsidecar code; the C++ side does not call this
// directly.
extern int TC9InvokeCreatureTemplateRowHandler(const CreatureTemplateRow* row);

#ifdef __cplusplus
}
#endif

#endif // LIBSIDECAR_WORLD_DB_H
