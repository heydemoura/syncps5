#pragma once

/* Adds the Syncthing shortcut to the Media tab of the home screen the first
 * time the payload runs. Does nothing when built without -DICON_HELPER. */
void home_icon_install_once(void);
