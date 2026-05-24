#!/bin/bash

# Certbot deployment hook for nginx
# This script is called by certbot when certificates are renewed

# Reload nginx to pick up new certificates
if systemctl is-active --quiet nginx; then
    echo "Reloading nginx to apply new certificates..."
    systemctl reload nginx
    
    # Verify nginx is still running
    if systemctl is-active --quiet nginx; then
        logger "Certbot: Successfully reloaded nginx with new certificates"
        echo "nginx reloaded successfully"
    else
        logger "Certbot: ERROR - nginx failed to reload after certificate renewal"
        echo "ERROR: nginx failed to reload"
        exit 1
    fi
else
    echo "nginx is not running - starting nginx..."
    systemctl start nginx
fi

exit 0