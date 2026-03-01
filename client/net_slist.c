/*
 * net_slist.c -- server list helpers
 *
 * All slist/smenu logic that doesn't modify upstream functions lives here:
 * dynamic column layout, format helpers, hostcache name resolution, and
 * console print wrappers.  Patches only need thin one-liner callsites.
 */

#include "quakedef.h"

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
	qboolean show_pool;
} slist_layout_t;

// -----------------------------------------------------------------------
// Aggregated server list parsing
// -----------------------------------------------------------------------

// Called from _Datagram_SearchForHosts immediately after CCREP_SERVER_INFO
// is confirmed. Reads the NexQuake aggregated payload, fills hostcache, and
// sets slist_agg_done so the poll loop short-circuits.
void NET_SlistParseAggregatedList(int ldriver)
{
	int s, num_servers, server_port, slot;
	char *server_port_text;
	struct qsockaddr addr;

	slist_agg_done = true;
	num_servers = MSG_ReadByte();
	for (s = 0; s < num_servers && hostCacheCount < HOSTCACHESIZE; s++)
	{
		server_port_text = MSG_ReadString();
		server_port = Q_atoi(server_port_text);
		if (server_port < 1 || server_port > 65535)
		{
			// Skip remaining fields in malformed entries.
			MSG_ReadString();
			MSG_ReadString();
			MSG_ReadString();
			MSG_ReadByte();
			MSG_ReadByte();
			MSG_ReadByte();
			MSG_ReadByte();
			MSG_ReadByte();
			MSG_ReadByte();
			MSG_ReadByte();
			continue;
		}

		// In NexQuake's WS transport, hostcache entries must use the same
		// virtual addressing scheme as direct port connects (so serverinfo
		// can match cls.netcon->addr back to hostcache[n].addr).
		Q_memset(&addr, 0, sizeof(addr));
		if (net_landrivers[ldriver].GetAddrFromName(server_port_text, &addr) == -1)
			net_landrivers[ldriver].SetSocketPort(&addr, server_port);

		slot = hostCacheCount++;
		hostcache[slot].instances = 1;
		Q_strncpy(hostcache[slot].name, MSG_ReadString(), sizeof(hostcache[slot].name) - 1);
		hostcache[slot].name[sizeof(hostcache[slot].name) - 1] = 0;
		Q_strncpy(hostcache[slot].map, MSG_ReadString(), sizeof(hostcache[slot].map) - 1);
		hostcache[slot].map[sizeof(hostcache[slot].map) - 1] = 0;
		Q_strncpy(hostcache[slot].gamedir, MSG_ReadString(), sizeof(hostcache[slot].gamedir) - 1);
		hostcache[slot].gamedir[sizeof(hostcache[slot].gamedir) - 1] = 0;
		hostcache[slot].users    = MSG_ReadByte() | (MSG_ReadByte() << 8);
		hostcache[slot].maxusers = MSG_ReadByte() | (MSG_ReadByte() << 8);
		hostcache[slot].instances = MSG_ReadByte() | (MSG_ReadByte() << 8);
		if (MSG_ReadByte() != NET_PROTOCOL_VERSION)
		{
			Q_strcpy(hostcache[slot].cname, hostcache[slot].name);
			hostcache[slot].cname[sizeof(hostcache[slot].cname) - 1] = 0;
			Q_strcpy(hostcache[slot].name, "*");
			Q_strcat(hostcache[slot].name, hostcache[slot].cname);
		}
		Q_memcpy(&hostcache[slot].addr, &addr, sizeof(struct qsockaddr));
		hostcache[slot].driver  = net_driverlevel;
		hostcache[slot].ldriver = ldriver;
		Q_strncpy(hostcache[slot].cname, va("%d", server_port), sizeof(hostcache[slot].cname) - 1);
		hostcache[slot].cname[sizeof(hostcache[slot].cname) - 1] = 0;
	}
}

// -----------------------------------------------------------------------
// Hostcache name resolution
// -----------------------------------------------------------------------

int NET_ResolveHostcacheName(char *token, char *out, int out_size)
{
	int i, match = -1;
	int token_len;

	if (!token || !token[0] || !out || out_size < 2)
		return 0;

	token_len = Q_strlen(token);

	for (i = 0; i < hostCacheCount; i++)
		if (!Q_strcasecmp(token, hostcache[i].name))
		{
			match = i;
			break;
		}

	if (match < 0)
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
	10, 8, 10, 3
};

// Prefer preserving columns by shrinking Server to this width before fallback.
#define SLIST_SERVER_SOFT_MIN 15

static int slist_users_width(qboolean show_pool)
{
	int i, w, max_w;
	char users[32];

	max_w = slist_col_min[SLIST_COL_USERS];
	for (i = 0; i < hostCacheCount; i++)
	{
		if (!hostcache[i].maxusers)
			continue;
		if (show_pool)
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

	layout->show_pool = true;
	users_w = slist_users_width(true);
	layout->ncols = slist_cols_for_budget(budget, users_w);
	if (!layout->ncols)
	{
		layout->show_pool = false;
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
	else if (layout->show_pool)
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
