// settings: function declarations shared globally with the other app/*.js files.

async function fetchMailboxSettings() {
    const response = await fetch('/api/mailbox-settings', { credentials: 'include' });
    checkResponse(response);
    const settings = await response.json();
    return settings;
}

// Refresh sidebar unseen counts for a specific account (or all if not specified).
async function refreshSidebarCounts(accountName) {
    try {
        const accounts = await fetchAccounts();
        const accountTabs = document.getElementById('account-tabs');
        const accountButtons = accountTabs.querySelectorAll('.account-tablinks');
        for (const account of accounts) {
            if (accountName && account.Name !== accountName) continue;
            // Update account badge
            for (const button of accountButtons) {
                if (button.dataset.account === account.Name) {
                    const existingBadge = button.querySelector('.account-unread-badge');
                    if (existingBadge) existingBadge.remove();
                    if (account.Unseen > 0) {
                        const badge = document.createElement('span');
                        badge.className = 'account-unread-badge';
                        badge.textContent = account.Unseen > 99 ? '99+' : account.Unseen;
                        button.appendChild(badge);
                    }
                    break;
                }
            }
            // Update mailbox counts
            const mailboxData = await fetchMailboxes(account.Name);
            for (const mailbox of mailboxData.mailboxes) {
                const status = await fetchMailboxStatus(account.Name, mailbox);
                const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
                spans.forEach(span => updateMailboxCountBadge(span, status));
            }
        }
    } catch(err) { console.warn('Failed to refresh sidebar counts:', err); }
}

function playNewMailSound() {
if (!newMailSoundEnabled) return;
try {
    if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    const now = audioCtx.currentTime;
    // First tone
    const osc1 = audioCtx.createOscillator();
    const gain1 = audioCtx.createGain();
    osc1.type = 'sine';
    osc1.frequency.value = 880; // A5
    gain1.gain.setValueAtTime(0.15, now);
    gain1.gain.exponentialRampToValueAtTime(0.001, now + 0.15);
    osc1.connect(gain1);
    gain1.connect(audioCtx.destination);
    osc1.start(now);
    osc1.stop(now + 0.15);
    // Second tone (higher)
    const osc2 = audioCtx.createOscillator();
    const gain2 = audioCtx.createGain();
    osc2.type = 'sine';
    osc2.frequency.value = 1175; // D6
    gain2.gain.setValueAtTime(0.15, now + 0.12);
    gain2.gain.exponentialRampToValueAtTime(0.001, now + 0.3);
    osc2.connect(gain2);
    gain2.connect(audioCtx.destination);
    osc2.start(now + 0.12);
    osc2.stop(now + 0.3);
} catch (e) { /* audio not available */ }
    }

// Notification permission — must be triggered by user gesture in modern browsers
    function updateNotifIcon() {
const onIcon = document.getElementById('notif-icon-on');
const offIcon = document.getElementById('notif-icon-off');
const deniedIcon = document.getElementById('notif-icon-denied');
const btn = document.getElementById('notif-toggle-btn');
if (!onIcon || !offIcon || !deniedIcon || !btn) return;
if (!('Notification' in window)) {
    btn.style.display = 'none';
    return;
}
const perm = Notification.permission;
// When permission is granted, the on/off icon reflects the app-level toggle.
const showOn = perm === 'granted' && notificationsEnabled;
const showDefault = perm === 'default' || (perm === 'granted' && !notificationsEnabled);
onIcon.style.display = showOn ? '' : 'none';
offIcon.style.display = showDefault ? '' : 'none';
deniedIcon.style.display = perm === 'denied' ? '' : 'none';
const label = document.getElementById('notif-label');
if (perm === 'granted') {
    btn.title = notificationsEnabled ? 'Notifications on (click to disable)' : 'Notifications off (click to enable)';
    if (label) label.textContent = notificationsEnabled ? 'Notifications: On' : 'Notifications: Off';
} else if (perm === 'denied') {
    btn.title = 'Notifications blocked (change in browser settings)';
    if (label) label.textContent = 'Notifications: Blocked';
} else {
    btn.title = 'Enable browser notifications';
    if (label) label.textContent = 'Notifications';
}
    }

// New-mail sound toggle (persisted per-browser).
    function updateSoundIcon() {
const onIcon = document.getElementById('sound-icon-on');
const offIcon = document.getElementById('sound-icon-off');
const btn = document.getElementById('sound-toggle-btn');
const label = document.getElementById('sound-label');
if (!onIcon || !offIcon || !btn) return;
onIcon.style.display = newMailSoundEnabled ? '' : 'none';
offIcon.style.display = newMailSoundEnabled ? 'none' : '';
btn.title = newMailSoundEnabled ? 'New-mail sound on (click to mute)' : 'New-mail sound off (click to enable)';
if (label) label.textContent = newMailSoundEnabled ? 'Sound: On' : 'Sound: Off';
    }

function closeSettingsMenu() {
    document.getElementById('settings-menu').classList.remove('open');
}
