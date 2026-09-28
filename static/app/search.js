// search: function declarations shared globally with the other app/*.js files.

async function runSearch() {
        const query = document.getElementById('search-input').value;
        if (!query.trim()) return;
        const accountName = currentMailbox.account;
        const scopeBtn = document.getElementById('search-scope');
        const scope = scopeBtn.dataset.scope;
        let url = `/api/search?account=${encodeURIComponent(accountName)}&query=${encodeURIComponent(query)}`;
        if (scope === 'current') {
            url += `&mailbox=${encodeURIComponent(currentMailbox.mailbox)}`;
        }

        // Show search pane with a loading state immediately.
        openTab(null, 'search-content', 'mail-tabcontent', 'mail-tablinks');
        const searchResultsDiv = document.getElementById('search-results');
        searchResultsDiv.innerHTML = '<div class="no-mail-message"><span class="search-spinner"></span>Searching…</div>';
        const searchBar = document.querySelector('.search-bar');
        searchBar.classList.add('searching');

        let results;
        try {
            const response = await fetch(url, { credentials: 'include' });
            checkResponse(response);
            results = await response.json();
        } finally {
            searchBar.classList.remove('searching');
        }

        searchResultsDiv.innerHTML = '';

        if (!results || results.length === 0) {
            searchResultsDiv.innerHTML = '<div class="no-mail-message">No results found</div>';
            return;
        }

        const scopeLabel = scope === 'current' ? currentMailbox.mailbox : 'All mailboxes';
        searchResultsDiv.innerHTML = `<div class="mailbox-toolbar"><div class="mailbox-header"><span class="mailbox-name">Search: "${escapeHtml(query)}" in ${escapeHtml(scopeLabel)}</span><span class="mailbox-stats">${results.length} found</span></div></div><div id="search-results-scroll"></div>`;
        const scrollDiv = document.getElementById('search-results-scroll');

        results.forEach(mail => {
            const from = mail.from && mail.from.length > 0 ? mail.from[0].PersonalName || mail.from[0].MailboxName : '';
            const date = new Date(mail.date);
            const today = new Date();
            const isToday = date.toDateString() === today.toDateString();
            const timeString = isToday ? date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
            const isRead = mail.flags && mail.flags.includes('\\Seen');
            const subject = mail.subject || '[No Subject]';
            const hasAttachment = mail.attachments && mail.attachments.length > 0;

            const row = document.createElement('div');
            row.className = `mail-row ${isRead ? 'read' : 'unread'}`;
            row.tabIndex = 0;
            row.dataset.account = accountName;
            row.dataset.mailbox = mail.mailbox;
            row.dataset.uid = mail.uid;

            const avatar = document.createElement('div');
            avatar.className = 'mail-avatar';
            avatar.style.backgroundColor = getAvatarColor(from);
            avatar.textContent = getInitials(from);
            row.appendChild(avatar);

            const senderDiv = document.createElement('div');
            senderDiv.className = 'mail-sender';
            senderDiv.textContent = from;
            row.appendChild(senderDiv);

            const subjectArea = document.createElement('div');
            subjectArea.className = 'mail-subject-area';
            const subjectText = document.createElement('span');
            subjectText.className = 'mail-subject-text';
            subjectText.textContent = subject;
            subjectArea.appendChild(subjectText);
            appendOrderedChips(subjectArea, mail.labels, mail.flags);
            const mailboxTag = document.createElement('span');
            mailboxTag.className = 'mail-snippet';
            mailboxTag.textContent = ` \u2014 ${mail.mailbox}`;
            subjectArea.appendChild(mailboxTag);
            row.appendChild(subjectArea);

            if (hasAttachment) {
                const attachIcon = document.createElement('span');
                attachIcon.className = 'mail-attachment-icon';
                attachIcon.innerHTML = svgAttachmentIcon(16);
                row.appendChild(attachIcon);
            }

            const dateDiv = document.createElement('div');
            dateDiv.className = 'mail-date';
            dateDiv.textContent = timeString;
            row.appendChild(dateDiv);

            row.addEventListener('click', (e) => {
                showMail(accountName, mail.mailbox, mail.uid, e);
            });
            row.addEventListener('keydown', (e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    row.click();
                }
            });

            scrollDiv.appendChild(row);
        });
}
