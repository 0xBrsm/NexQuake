/*
 * net_slist.c -- server list helpers
 *
 * All slist/smenu logic that doesn't modify upstream functions lives here:
 * dynamic column layout, format helpers, hostcache name resolution, and
 * console print wrappers.  Patches only need thin one-liner callsites.
 */

#include "quakedef.h"

#ifdef __EMSCRIPTEN__
#include <emscripten.h>
#else
#ifndef EMSCRIPTEN_KEEPALIVE
#define EMSCRIPTEN_KEEPALIVE
#endif
#endif

// Set true once the first SSE snapshot lands, clearing the search-menu
// "Searching..." spinner at cold boot (the cache is warm from then on). (Named
// for the retired aggregated wire path; kept as the first-snapshot flag so
// net.h is untouched.)
qboolean slist_agg_done = false;

#define SLIST_COL_SERVER  0
#define SLIST_COL_GAME    1
#define SLIST_COL_MAP     2
#define SLIST_COL_USERS   3
#define SLIST_NUM_COLS    4

typedef struct {
	int  ncols;
	int  col_order[SLIST_NUM_COLS];
	int  col_width[SLIST_NUM_COLS];
	int  total_width;
	qboolean show_instances;
} slist_layout_t;

// -----------------------------------------------------------------------
// Server list ingest (SSE). The browser's EventSource('/events') stream is
// parsed in the JS shell (56-sse.js), which drives Begin -> IngestEntry* ->
// Commit to repopulate hostcache. No Quake wire protocol is involved.
//
// These run from the EventSource onmessage callback, so they only touch
// hostcache memory and the menu/console flags — no engine re-entry, no socket
// operations (the asyncify callback contract).
// -----------------------------------------------------------------------

extern qboolean slistInProgress; // net_main.c
extern qboolean slist_sorted;    // menu.c

// Pick an initialized landriver to resolve the virtual address for connect.
static int NET_SlistLandriver(void)
{
	int i;
	for (i = 0; i < net_numlandrivers; i++)
		if (net_landrivers[i].initialized)
			return i;
	return 0;
}

EMSCRIPTEN_KEEPALIVE void NET_SlistBegin(void)
{
	hostCacheCount = 0;
}

EMSCRIPTEN_KEEPALIVE void NET_SlistIngestEntry(int port, const char *name,
		const char *map, const char *gamedir, int users, int maxusers,
		int instances)
{
	int slot, ldriver;
	struct qsockaddr addr;
	char portstr[16];

	if (port < 1 || port > 65535 || hostCacheCount >= HOSTCACHESIZE)
		return;

	sprintf(portstr, "%d", port);
	ldriver = NET_SlistLandriver();

	// Match the virtual addressing of direct port connects so in-game
	// serverinfo can map cls.netcon->addr back to this hostcache entry.
	Q_memset(&addr, 0, sizeof(addr));
	if (net_landrivers[ldriver].GetAddrFromName(portstr, &addr) == -1)
		net_landrivers[ldriver].SetSocketPort(&addr, port);

	slot = hostCacheCount++;
	Q_strncpy(hostcache[slot].name, (char *)(name ? name : ""), sizeof(hostcache[slot].name) - 1);
	hostcache[slot].name[sizeof(hostcache[slot].name) - 1] = 0;
	Q_strncpy(hostcache[slot].map, (char *)(map ? map : ""), sizeof(hostcache[slot].map) - 1);
	hostcache[slot].map[sizeof(hostcache[slot].map) - 1] = 0;
	Q_strncpy(hostcache[slot].gamedir, (char *)(gamedir ? gamedir : ""), sizeof(hostcache[slot].gamedir) - 1);
	hostcache[slot].gamedir[sizeof(hostcache[slot].gamedir) - 1] = 0;
	hostcache[slot].users    = users;
	hostcache[slot].maxusers = maxusers;
	hostcache[slot].instances = instances;
	Q_memcpy(&hostcache[slot].addr, &addr, sizeof(struct qsockaddr));
	hostcache[slot].driver  = 0;
	hostcache[slot].ldriver = ldriver;
	// connect target is the port string (connect re-resolves it per driver).
	Q_strncpy(hostcache[slot].cname, portstr, sizeof(hostcache[slot].cname) - 1);
	hostcache[slot].cname[sizeof(hostcache[slot].cname) - 1] = 0;
}

EMSCRIPTEN_KEEPALIVE void NET_SlistCommit(void)
{
	slist_agg_done = true;  // first snapshot received
	slist_sorted = false;   // force the server browser to re-sort/repaint
	slistInProgress = false;
}

// Cold-boot gate for the `slist` console command: block (asyncify) until the
// always-on SSE stream (started by the JS shell at runtime init) delivers its
// first snapshot, so the first printed list isn't empty before the stream has
// connected. Fires at most once per session — slist_agg_done stays set after,
// so steady-state slist never blocks. No-op on native builds (no stream, and
// emscripten_sleep is the only yield that lets the JS callback run). See DEC-020.
void NET_SlistAwaitFirstSnapshot(void)
{
#ifdef __EMSCRIPTEN__
	double start = Sys_FloatTime();
	while (!slist_agg_done && (Sys_FloatTime() - start) < 2.0)
		emscripten_sleep(16);
#endif
}

// -----------------------------------------------------------------------
// Hostcache name resolution. Callers can require exact matches for explicit
// rcon targeting or allow fuzzy prefix matching for connect-style UX.
// -----------------------------------------------------------------------

int NET_ResolveHostcacheName(char *token, char *out, int out_size, qboolean exact)
{
	int i, match = -1;
	int token_len;

	if (!token || !token[0] || !out || out_size < 2)
		return 0;

	token_len = Q_strlen(token);

	for (i = 0; i < hostCacheCount; i++)
	{
		if (!Q_strcasecmp(token, hostcache[i].name))
		{
			match = i;
			break;
		}
	}

	if (match < 0 && !exact)
	{
		for (i = 0; i < hostCacheCount; i++)
		{
			if (Q_strncasecmp(token, hostcache[i].name, token_len))
				continue;
			if (match >= 0)
				return -1;
			match = i;
		}
	}

	if (match < 0)
		return 0;

	Q_strncpy(out, hostcache[match].cname, out_size - 1);
	out[out_size - 1] = 0;
	return match + 1;
}

// -----------------------------------------------------------------------
// Dynamic column layout
// -----------------------------------------------------------------------

// Column name strings (indexed by SLIST_COL_*).
static const char *slist_col_name[SLIST_NUM_COLS] = {
	"Server", "Game", "Map", "Users"
};

// Preferred widths when there is enough room.
static const int slist_col_max[SLIST_NUM_COLS] = {
	23, 15, 15, 19
};

// Minimum useful widths for narrow layouts.
static const int slist_col_min[SLIST_NUM_COLS] = {
	10, 8, 10, 5
};

// Prefer preserving columns by shrinking Server to this width before fallback.
#define SLIST_SERVER_SOFT_MIN 15

static int slist_users_width(qboolean show_instances)
{
	int i, w, max_w;
	char users[32];

	max_w = slist_col_min[SLIST_COL_USERS];
	for (i = 0; i < hostCacheCount; i++)
	{
		if (!hostcache[i].maxusers)
			continue;
		if (show_instances && hostcache[i].instances > 0)
			w = sprintf(users, "%u/%u (%u)", hostcache[i].users,
				hostcache[i].maxusers, hostcache[i].instances);
		else
			w = sprintf(users, "%u/%u", hostcache[i].users,
				hostcache[i].maxusers);
		if (w > max_w)
			max_w = w;
	}

	if (max_w > slist_col_max[SLIST_COL_USERS])
		max_w = slist_col_max[SLIST_COL_USERS];
	return max_w;
}

static void slist_shrink_col(slist_layout_t *layout, int col, int min_width, int *over)
{
	int shrink;

	if (*over <= 0)
		return;

	shrink = layout->col_width[col] - min_width;
	if (shrink <= 0)
		return;
	if (shrink > *over)
		shrink = *over;
	layout->col_width[col] -= shrink;
	*over -= shrink;
}

static int slist_cols_for_budget(int budget, int users_w)
{
	int min2, min3, min4;

	min2 = slist_col_min[SLIST_COL_SERVER] + slist_col_min[SLIST_COL_USERS] + 1;
	min3 = slist_col_min[SLIST_COL_SERVER] + slist_col_min[SLIST_COL_GAME] + users_w + 2;
	min4 = min3 + slist_col_min[SLIST_COL_MAP] + 1;

	if (budget >= min4)
		return 4;
	if (budget >= min3)
		return 3;
	if (budget >= min2)
		return 2;
	return 0;
}

static void NET_SlistBuildLayout(int budget, slist_layout_t *layout)
{
	int i, over, users_w;

	if (budget < 16)
		budget = 16;

	Q_memset(layout, 0, sizeof(*layout));

	for (i = 0; i < hostCacheCount; i++)
		if (hostcache[i].instances > 0)
		{
			layout->show_instances = true;
			break;
		}
	users_w = slist_users_width(layout->show_instances);
	layout->ncols = slist_cols_for_budget(budget, users_w);
	if (layout->show_instances && !layout->ncols)
	{
		layout->show_instances = false;
		users_w = slist_users_width(false);
		layout->ncols = slist_cols_for_budget(budget, users_w);
	}

	layout->col_order[0] = SLIST_COL_SERVER;
	layout->col_order[1] = SLIST_COL_USERS;
	if (layout->ncols == 4)
	{
		layout->col_order[1] = SLIST_COL_GAME;
		layout->col_order[2] = SLIST_COL_MAP;
		layout->col_order[3] = SLIST_COL_USERS;
	}
	else if (layout->ncols == 3)
	{
		layout->col_order[1] = SLIST_COL_GAME;
		layout->col_order[2] = SLIST_COL_USERS;
	}

	layout->col_width[SLIST_COL_USERS] = users_w;
	layout->col_width[SLIST_COL_SERVER] = slist_col_max[SLIST_COL_SERVER];
	if (layout->ncols >= 3)
		layout->col_width[SLIST_COL_GAME] = slist_col_max[SLIST_COL_GAME];
	if (layout->ncols == 4)
		layout->col_width[SLIST_COL_MAP] = slist_col_max[SLIST_COL_MAP];

	layout->total_width = 0;
	for (i = 0; i < layout->ncols; i++)
		layout->total_width += layout->col_width[layout->col_order[i]];
	layout->total_width += layout->ncols - 1; // separators

	over = layout->total_width - budget;
	if (layout->ncols == 4)
	{
		slist_shrink_col(layout, SLIST_COL_SERVER, SLIST_SERVER_SOFT_MIN, &over);
		slist_shrink_col(layout, SLIST_COL_MAP, slist_col_min[SLIST_COL_MAP], &over);
		slist_shrink_col(layout, SLIST_COL_GAME, slist_col_min[SLIST_COL_GAME], &over);
		slist_shrink_col(layout, SLIST_COL_SERVER, slist_col_min[SLIST_COL_SERVER], &over);
	}
	else if (layout->ncols == 3)
	{
		slist_shrink_col(layout, SLIST_COL_SERVER, SLIST_SERVER_SOFT_MIN, &over);
		slist_shrink_col(layout, SLIST_COL_GAME, slist_col_min[SLIST_COL_GAME], &over);
		slist_shrink_col(layout, SLIST_COL_SERVER, slist_col_min[SLIST_COL_SERVER], &over);
	}
	else
	{
		slist_shrink_col(layout, SLIST_COL_USERS, slist_col_min[SLIST_COL_USERS], &over);
		slist_shrink_col(layout, SLIST_COL_SERVER, slist_col_min[SLIST_COL_SERVER], &over);
	}

	layout->total_width = 0;
	for (i = 0; i < layout->ncols; i++)
		layout->total_width += layout->col_width[layout->col_order[i]];
	layout->total_width += layout->ncols - 1;
}

// Format one column, left-justified, truncated to width.
static int slist_write_col(char *out, int outsz, const char *text, int width)
{
	int len, pad, written;

	len = Q_strlen((char *)text);
	if (len > width)
		len = width;
	if (len >= outsz)
		len = outsz - 1;
	Q_memcpy(out, (char *)text, len);

	pad = width - len;
	if (pad > outsz - len - 1)
		pad = outsz - len - 1;
	if (pad > 0)
		Q_memset(out + len, ' ', pad);
	written = len + (pad > 0 ? pad : 0);
	out[written] = '\0';
	return written;
}

static void slist_rstrip_spaces(char *text)
{
	int len;

	len = Q_strlen(text);
	while (len > 0 && text[len - 1] == ' ')
		text[--len] = 0;
}

static int NET_SlistFormatHeader(const slist_layout_t *layout, char *out, int outsz)
{
	int i, p = 0;
	for (i = 0; i < layout->ncols && p < outsz - 1; i++)
	{
		if (i > 0 && p < outsz - 1)
			out[p++] = ' ';
		p += slist_write_col(out + p, outsz - p,
			slist_col_name[layout->col_order[i]],
			layout->col_width[layout->col_order[i]]);
	}
	out[p] = '\0';
	return p;
}

static int NET_SlistFormatDivider(const slist_layout_t *layout, char *out, int outsz)
{
	int i, w, p = 0;
	for (i = 0; i < layout->ncols && p < outsz - 1; i++)
	{
		if (i > 0 && p < outsz - 1)
			out[p++] = ' ';
		w = layout->col_width[layout->col_order[i]];
		if (w > outsz - p - 1)
			w = outsz - p - 1;
		Q_memset(out + p, '-', w);
		p += w;
	}
	out[p] = '\0';
	return p;
}

static int NET_SlistFormatEntry(const slist_layout_t *layout, const hostcache_t *host,
		char *out, int outsz)
{
	int i, col, p = 0;
	int users_w, inst_w, gap, k;
	char users[64];
	char inst[24];
	const char *text;

	// Pre-format Users string.
	if (!host->maxusers)
		Q_strcpy(users, "");
	else if (layout->show_instances && host->instances > 0)
	{
		users_w = sprintf(users, "%u/%u", host->users, host->maxusers);
		inst_w = sprintf(inst, "(%u)", host->instances);
		gap = layout->col_width[SLIST_COL_USERS] - users_w - inst_w;
		if (gap < 1)
			gap = 1;
		while (gap-- > 0 && users_w < (int)sizeof(users) - 1)
			users[users_w++] = ' ';
		users[users_w] = 0;
		for (k = 0; inst[k] && users_w < (int)sizeof(users) - 1; k++)
			users[users_w++] = inst[k];
		users[users_w] = 0;
	}
	else
		sprintf(users, "%u/%u", host->users, host->maxusers);

	for (i = 0; i < layout->ncols && p < outsz - 1; i++)
	{
		if (i > 0 && p < outsz - 1)
			out[p++] = ' ';
		col = layout->col_order[i];
		text = users;
		if (col == SLIST_COL_SERVER)
			text = host->name;
		else if (col == SLIST_COL_MAP)
			text = host->map;
		else if (col == SLIST_COL_GAME)
			text = host->gamedir;
		p += slist_write_col(out + p, outsz - p, text,
			layout->col_width[col]);
	}
	out[p] = '\0';
	return p;
}

// -----------------------------------------------------------------------
// Console print wrappers (called from patched PrintSlistHeader / PrintSlist)
// -----------------------------------------------------------------------

void NET_SlistPrintHeader(int budget)
{
	slist_layout_t layout;
	char line[128];

	// Blank line above the header for separation from prior console output —
	// stock got this from the "Looking for Quake servers..." line, which is gone
	// now that slist is an instant cache read rather than a LAN search.
	Con_Printf("\n");

	NET_SlistBuildLayout(budget, &layout);
	NET_SlistFormatHeader(&layout, line, sizeof(line));
	slist_rstrip_spaces(line);
	Con_Printf("%s\n", line);
	NET_SlistFormatDivider(&layout, line, sizeof(line));
	Con_Printf("%s\n", line);
}

void NET_SlistPrintEntry(int budget, const hostcache_t *host)
{
	slist_layout_t layout;
	char line[128];

	NET_SlistBuildLayout(budget, &layout);
	NET_SlistFormatEntry(&layout, host, line, sizeof(line));
	slist_rstrip_spaces(line);
	Con_Printf("%s\n", line);
}

// -----------------------------------------------------------------------
// Budget-based format wrappers for callers that cannot see slist_layout_t
// (e.g. menu.c, which formats to a string rather than printing to console).
// -----------------------------------------------------------------------

int NET_SlistWidth(int budget)
{
	slist_layout_t layout;
	NET_SlistBuildLayout(budget, &layout);
	return layout.total_width;
}

int NET_SlistFormatHeaderLine(int budget, char *out, int outsz)
{
	slist_layout_t layout;
	NET_SlistBuildLayout(budget, &layout);
	return NET_SlistFormatHeader(&layout, out, outsz);
}

int NET_SlistFormatEntryLine(int budget, const hostcache_t *host, char *out, int outsz)
{
	slist_layout_t layout;
	NET_SlistBuildLayout(budget, &layout);
	return NET_SlistFormatEntry(&layout, host, out, outsz);
}
