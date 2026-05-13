#include "world-db.h"
#include <stddef.h>

// Single-process handler slot. AC registers the handler once at startup
// before ObjectMgr::LoadCreatureTemplates runs. Concurrent registration is
// not supported (and isn't needed -- AC's loader is single-threaded).
static CreatureTemplateRowHandler g_creatureTemplateRowHandler = NULL;

void TC9SetCreatureTemplateRowHandler(CreatureTemplateRowHandler h) {
    g_creatureTemplateRowHandler = h;
}

int TC9InvokeCreatureTemplateRowHandler(const CreatureTemplateRow* row) {
    if (g_creatureTemplateRowHandler == NULL || row == NULL) {
        return 0;
    }
    return g_creatureTemplateRowHandler(row);
}
