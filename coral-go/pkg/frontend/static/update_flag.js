/* The switch for the auto-update UI (#5275): off until the updater lands. It is read at startup and again where the UI hangs itself on a page (Settings' tab list), so it has no imports but the storage wrapper.
 * Turn it on in this browser with localStorage.setItem('coral-updates-ui', 'on') (the suites do); when the server grows the updater, its `enabled` answer replaces this. */
import { browserLocal } from './safe_storage.js';

export const UPDATES_FLAG = 'coral-updates-ui';
export const updatesUiOn = () => browserLocal.get(UPDATES_FLAG) === 'on';
