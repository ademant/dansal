// Service Worker for Dansal Web PWA (#259)
// Provides offline capabilities and caching

const CACHE_NAME = 'dansal-web-v1';
const ASSETS_TO_CACHE = [
  '/',
  '/index.html',
  '/static/css/main.css',
  '/static/js/main.js',
  '/static/images/logo.svg',
  '/static/images/banner.svg',
  '/static/images/favicon.svg',
  '/static/manifest.json',
];

// Install event - cache core assets
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then((cache) => {
        console.log('Caching core assets');
        return cache.addAll(ASSETS_TO_CACHE);
      })
      .then(() => {
        console.log('Service worker installed and assets cached');
        return self.skipWaiting(); // Activate immediately
      })
      .catch((err) => {
        console.error('Failed to cache assets:', err);
      })
  );
});

// Activate event - clean up old caches
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((cacheNames) => {
      return Promise.all(
        cacheNames.map((cacheName) => {
          if (cacheName !== CACHE_NAME) {
            console.log('Deleting old cache:', cacheName);
            return caches.delete(cacheName);
          }
        })
      );
    }).then(() => {
      console.log('Service worker activated');
      return self.clients.claim(); // Take control of all clients
    })
  );
});

// Fetch event - serve from cache or network
self.addEventListener('fetch', (event) => {
  // Skip non-GET requests
  if (event.request.method !== 'GET') {
    return;
  }

  // Try network first, fall back to cache (network-first strategy)
  event.respondWith(
    fetch(event.request)
      .then((response) => {
        // Clone and cache successful responses
        if (response && response.status === 200 && response.type === 'basic') {
          const responseToCache = response.clone();
          caches.open(CACHE_NAME)
            .then((cache) => {
              cache.put(event.request, responseToCache);
            });
        }
        return response;
      })
      .catch(() => {
        // Offline - serve from cache
        return caches.match(event.request)
          .then((response) => {
            return response || fetch(event.request); // Fallback to network if not in cache
          });
      })
  );
});

// Background sync for failed requests (when connection is restored)
self.addEventListener('sync', (event) => {
  if (event.tag === 'sync-failed-requests') {
    event.waitUntil(syncFailedRequests());
  }
});

async function syncFailedRequests() {
  const failedRequests = await getFailedRequests();
  if (failedRequests.length === 0) {
    return;
  }

  for (const requestData of failedRequests) {
    try {
      const response = await fetch(requestData.url, {
        method: requestData.method,
        headers: requestData.headers,
        body: requestData.body,
      });
      
      if (response.ok) {
        // Request succeeded, remove from failed requests
        await removeFailedRequest(requestData.id);
        
        // Notify user if this was a visible request
        if (requestData.showNotification) {
          self.registration.showNotification('Request Synced', {
            body: 'Your data was successfully synced!',
            icon: '/static/images/logo.svg',
          });
        }
      }
    } catch (error) {
      console.error('Failed to sync request:', error);
    }
  }
}

// Helper functions for IndexedDB would go here
// In a real implementation, these would store/Retrieve failed requests
async function getFailedRequests() {
  // TODO: Implement IndexedDB storage for failed requests
  return [];
}

async function removeFailedRequest(id) {
  // TODO: Implement removal from IndexedDB
}

// Push notification support
self.addEventListener('push', (event) => {
  const data = event.data.json();
  const options = {
    body: data.body,
    icon: '/static/images/logo.svg',
    badge: '/static/images/badge.png',
    data: {
      url: data.url,
    },
  };
  
  event.waitUntil(
    self.registration.showNotification(data.title, options)
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  
  if (event.notification.data.url) {
    clients.openWindow(event.notification.data.url);
  }
});