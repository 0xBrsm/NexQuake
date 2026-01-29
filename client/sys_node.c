/*
 * sys_node.c - System layer for headless Node.js WASM client
 *
 * Based on sys_sdl.c but without SDL dependencies.
 * Used for scripted testing via Nexus.
 */

#include <unistd.h>
#include <signal.h>
#include <stdlib.h>
#include <limits.h>
#include <sys/time.h>
#include <sys/types.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <ctype.h>
#include <errno.h>
#include <sys/stat.h>

#include "quakedef.h"

#ifdef __EMSCRIPTEN__
#include <emscripten.h>
#endif

qboolean isDedicated;

int noconinput = 0;

char *basedir = ".";
char *cachedir = "/tmp";

cvar_t sys_linerefresh = {"sys_linerefresh", "0"};
cvar_t sys_nostdout = {"sys_nostdout", "0"};

// Shared variables for main loop
static double time_val, oldtime, newtime;
extern int vcrFile;
extern int recording;
static int frame;

// Forward declaration
void main_loop(void);

#ifdef __EMSCRIPTEN__
static qboolean init_complete = false;
#endif

// =======================================================================
// General routines
// =======================================================================

void Sys_DebugNumber(int y, int val)
{
}

void Sys_Printf(char *fmt, ...)
{
    va_list argptr;
    char text[1024];

    va_start(argptr, fmt);
    vsprintf(text, fmt, argptr);
    va_end(argptr);
    fprintf(stdout, "%s", text);
    fflush(stdout);
}

void Sys_Quit(void)
{
    Host_Shutdown();
    exit(0);
}

void Sys_Init(void)
{
#if id386
    Sys_SetFPCW();
#endif
}

#if !id386

void Sys_LowFPPrecision(void)
{
}

void Sys_HighFPPrecision(void)
{
}

#endif // !id386

void Sys_Error(char *error, ...)
{
    va_list argptr;
    char string[1024];

    va_start(argptr, error);
    vsprintf(string, error, argptr);
    va_end(argptr);
    fprintf(stderr, "Error: %s\n", string);
    fflush(stderr);

    Host_Shutdown();
    exit(1);
}

void Sys_Warn(char *warning, ...)
{
    va_list argptr;
    char string[1024];

    va_start(argptr, warning);
    vsprintf(string, warning, argptr);
    va_end(argptr);
    fprintf(stderr, "Warning: %s", string);
    fflush(stderr);
}

/*
===============================================================================

FILE IO

===============================================================================
*/

#define MAX_HANDLES 10
FILE *sys_handles[MAX_HANDLES];

int findhandle(void)
{
    int i;

    for (i = 1; i < MAX_HANDLES; i++)
        if (!sys_handles[i])
            return i;
    Sys_Error("out of handles");
    return -1;
}

static int Qfilelength(FILE *f)
{
    int pos;
    int end;

    pos = ftell(f);
    fseek(f, 0, SEEK_END);
    end = ftell(f);
    fseek(f, pos, SEEK_SET);

    return end;
}

int Sys_FileOpenRead(char *path, int *hndl)
{
    FILE *f;
    int i;

    i = findhandle();

    f = fopen(path, "rb");
    if (!f)
    {
        *hndl = -1;
        return -1;
    }
    sys_handles[i] = f;
    *hndl = i;

    return Qfilelength(f);
}

int Sys_FileOpenWrite(char *path)
{
    FILE *f;
    int i;

    i = findhandle();

    f = fopen(path, "wb");
    if (!f)
        Sys_Error("Error opening %s: %s", path, strerror(errno));
    sys_handles[i] = f;

    return i;
}

void Sys_FileClose(int handle)
{
    if (handle >= 0)
    {
        fclose(sys_handles[handle]);
        sys_handles[handle] = NULL;
    }
}

void Sys_FileSeek(int handle, int position)
{
    if (handle >= 0)
    {
        fseek(sys_handles[handle], position, SEEK_SET);
    }
}

int Sys_FileRead(int handle, void *dst, int count)
{
    char *data;
    int size, done;

    size = 0;
    if (handle >= 0)
    {
        data = dst;
        while (count > 0)
        {
            done = fread(data, 1, count, sys_handles[handle]);
            if (done == 0)
            {
                break;
            }
            data += done;
            count -= done;
            size += done;
        }
    }
    return size;
}

int Sys_FileWrite(int handle, void *src, int count)
{
    char *data;
    int size, done;

    size = 0;
    if (handle >= 0)
    {
        data = src;
        while (count > 0)
        {
            done = fwrite(data, 1, count, sys_handles[handle]);
            if (done == 0)
            {
                break;
            }
            data += done;
            count -= done;
            size += done;
        }
    }
    return size;
}

int Sys_FileTime(char *path)
{
    FILE *f;

    f = fopen(path, "rb");
    if (f)
    {
        fclose(f);
        return 1;
    }

    return -1;
}

void Sys_mkdir(char *path)
{
    mkdir(path, 0777);
}

void Sys_DebugLog(char *file, char *fmt, ...)
{
    va_list argptr;
    static char data[1024];
    FILE *fp;

    va_start(argptr, fmt);
    vsprintf(data, fmt, argptr);
    va_end(argptr);
    fp = fopen(file, "a");
    if (fp)
    {
        fwrite(data, strlen(data), 1, fp);
        fclose(fp);
    }
}

double Sys_FloatTime(void)
{
    struct timeval tp;
    struct timezone tzp;
    static int secbase;

    gettimeofday(&tp, &tzp);

    if (!secbase)
    {
        secbase = tp.tv_sec;
        return tp.tv_usec / 1000000.0;
    }

    return (tp.tv_sec - secbase) + tp.tv_usec / 1000000.0;
}

char *Sys_ConsoleInput(void)
{
    return 0;
}

// =======================================================================
// Sleeps for microseconds
// =======================================================================

static volatile int oktogo;

void alarm_handler(int x)
{
    oktogo = 1;
}

byte *Sys_ZoneBase(int *size)
{
    char *QUAKEOPT = getenv("QUAKEOPT");

    *size = 0xc00000;
    if (QUAKEOPT)
    {
        while (*QUAKEOPT)
            if (tolower(*QUAKEOPT++) == 'm')
            {
                *size = atof(QUAKEOPT) * 1024 * 1024;
                break;
            }
    }
    return malloc(*size);
}

void Sys_LineRefresh(void)
{
}

void Sys_Sleep(void)
{
    // In Node.js/Emscripten, sleeping is handled by the main loop scheduler
}

void floating_point_exception_handler(int whatever)
{
    signal(SIGFPE, floating_point_exception_handler);
}

void moncontrol(int x)
{
}

// Stub for input - headless has no keyboard events
void Sys_SendKeyEvents(void)
{
}

#ifdef __EMSCRIPTEN__
// Called from JavaScript when VFS initialization is complete
EMSCRIPTEN_KEEPALIVE
void WebQuake_VFSReady(void)
{
    init_complete = true;
}

// Note: WebQuake_ExecCommand and WebQuake_OnPageHide are defined in net_websocket.c
#endif

int main(int c, char **v)
{
    quakeparms_t parms;
    int pnum;

    moncontrol(0);

    signal(SIGFPE, SIG_IGN);

    COM_InitArgv(c, v);
    parms.argc = com_argc;
    parms.argv = com_argv;
    parms.memsize = 16 * 1024 * 1024;

    // Support for -mem and -heapsize parameters
    if ((pnum = COM_CheckParm("-mem")))
        parms.memsize = Q_atoi(com_argv[pnum + 1]) * 1024 * 1024;

    if ((pnum = COM_CheckParm("-heapsize")))
        parms.memsize = Q_atoi(com_argv[pnum + 1]) * 1024;

    parms.membase = malloc(parms.memsize);
    parms.basedir = basedir;
    parms.cachedir = NULL;

    Sys_Init();

    Host_Init(&parms);

    Con_Printf("\nQuake version 1.09 by id Software\n\n");
    Con_Printf("Headless Node.js client for scripted testing\n\n");
    Con_Printf("WebSocket multiplayer by initialed85 and brstm\n\n");

    if (com_argc > 1)
    {
        Con_Printf("Startup args:");

        for (pnum = 1; pnum < com_argc; pnum++)
            Con_Printf(" %s", com_argv[pnum]);

        Con_Printf("\n\n");
    }

    Cvar_RegisterVariable(&sys_nostdout);

    oldtime = Sys_FloatTime() - 0.1;

#ifdef __EMSCRIPTEN__
    // Use setImmediate-style timing for maximum speed in Node.js
    emscripten_set_main_loop_timing(EM_TIMING_SETIMMEDIATE, 0);
    emscripten_set_main_loop(main_loop, 0, 0);
#else
    while (1)
        main_loop();
#endif
}

void main_loop(void)
{
#ifdef __EMSCRIPTEN__
    // Wait for VFS initialization from JavaScript
    if (!init_complete)
    {
        return;
    }
#endif

    // find time spent rendering last frame
    newtime = Sys_FloatTime();
    time_val = newtime - oldtime;

    if (cls.state == ca_dedicated)
    {
        // play vcrfiles at max speed
        if (time_val < sys_ticrate.value && (vcrFile == -1 || recording))
        {
            Sys_Sleep();
            return; // not time to run a server only tic yet
        }
        time_val = sys_ticrate.value;
    }

    if (time_val > sys_ticrate.value * 2)
        oldtime = newtime;
    else
        oldtime += time_val;

    if (++frame > 10)
        moncontrol(1);
    Host_Frame(time_val);
    moncontrol(0);

    // graphic debugging aids
    if (sys_linerefresh.value)
        Sys_LineRefresh();
}

void Sys_MakeCodeWriteable(unsigned long startaddr, unsigned long length)
{
    // Not needed for WASM
}
