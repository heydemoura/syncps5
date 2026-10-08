/* syncps5 launcher: logging and on-screen notifications. */
#pragma once

#define SYNCPS5_DIR "/data/syncps5"
#define LAUNCHER_LOG SYNCPS5_DIR "/launcher.log"

/* Starts a new entry in the launcher log. */
void report_begin(void);

/* Writes a line to the launcher log and to whoever sent the payload. */
void report_log(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

/* Like report_log, and also shows a notification on the console. */
void report_fail(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

/* Shows a notification on the console. */
void report_notify(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

/* Points stderr at the launcher log, so that whatever the Go runtime prints
 * before Syncthing has opened its own log ends up there. */
void report_capture_stderr(void);
