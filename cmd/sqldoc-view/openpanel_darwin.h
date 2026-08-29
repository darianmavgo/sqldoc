// The open dialog, run by this application rather than borrowed from another.
// The result is delivered to sqldocPanelDone, which is implemented in Go.

#ifndef SQLDOC_OPENPANEL_H
#define SQLDOC_OPENPANEL_H

#include <stdint.h>

// sqldocOpenPanel returns immediately; the panel is shown on the main thread
// and the handle comes back with the answer.
void sqldocOpenPanel(const char *prompt, uintptr_t handle);

#endif
