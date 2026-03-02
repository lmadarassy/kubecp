#!/bin/sh
# PAM account script: create Linux user if not in /etc/passwd
# Called by pam_exec.so in the account phase after successful auth.
# Also writes to extrausers so subsequent getpwnam() calls succeed.
EXTRAUSERS_DIR="/var/lib/extrausers"
if [ -n "$PAM_USER" ] && ! id "$PAM_USER" >/dev/null 2>&1; then
    # Find next available UID (start from 2000)
    NEXT_UID=2000
    while getent passwd "$NEXT_UID" >/dev/null 2>&1; do
        NEXT_UID=$((NEXT_UID + 1))
    done
    useradd -M -d "/home/$PAM_USER" -s /usr/sbin/nologin -u "$NEXT_UID" "$PAM_USER" 2>/dev/null || true
    mkdir -p "/home/$PAM_USER"
    chown "$NEXT_UID:$NEXT_UID" "/home/$PAM_USER"
    # Add to extrausers
    if [ -d "$EXTRAUSERS_DIR" ]; then
        grep -q "^${PAM_USER}:" "$EXTRAUSERS_DIR/passwd" 2>/dev/null || \
            echo "${PAM_USER}:x:${NEXT_UID}:${NEXT_UID}::/home/${PAM_USER}:/usr/sbin/nologin" >> "$EXTRAUSERS_DIR/passwd"
        grep -q "^${PAM_USER}:" "$EXTRAUSERS_DIR/group" 2>/dev/null || \
            echo "${PAM_USER}:x:${NEXT_UID}:" >> "$EXTRAUSERS_DIR/group"
    fi
fi
exit 0
