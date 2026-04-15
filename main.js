const { app, BrowserWindow, dialog } = require('electron');
const { shell } = require('electron');
const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');

let mainWindow;
let backendProcess;
let pendingRoute = '';

const isWindows = process.platform === 'win32';
const gotSingleInstanceLock = app.requestSingleInstanceLock();

if (!gotSingleInstanceLock) {
  app.quit();
}

function getFrontendPath() {
  return path.join(__dirname, 'frontend', 'dist', 'index.html');
}

function normalizeRoute(route) {
  if (!route) {
    return '';
  }

  return route.startsWith('/') ? route : `/${route}`;
}

function parseDeepLink(argvOrUrl) {
  const candidate = Array.isArray(argvOrUrl)
    ? argvOrUrl.find((value) => typeof value === 'string' && value.startsWith('towel://'))
    : argvOrUrl;

  if (!candidate) {
    return '';
  }

  try {
    const parsed = new URL(candidate);
    const route = `${parsed.host ? `/${parsed.host}` : ''}${parsed.pathname || ''}${parsed.search || ''}`;
    return normalizeRoute(route);
  } catch (err) {
    console.error('Failed to parse deep link URL:', err);
    return '';
  }
}

function registerProtocol() {
  if (app.isPackaged) {
    app.setAsDefaultProtocolClient('towel');
    return;
  }

  if (process.defaultApp && process.argv.length >= 2) {
    app.setAsDefaultProtocolClient('towel', process.execPath, [path.resolve(process.argv[1])]);
    return;
  }

  app.setAsDefaultProtocolClient('towel');
}

function loadAppRoute(route = '') {
  const frontendPath = getFrontendPath();
  const normalizedRoute = normalizeRoute(route);

  if (!fs.existsSync(frontendPath)) {
    console.error('Frontend not found at:', frontendPath);
    dialog.showErrorBox('Frontend Error', `Frontend not found at: ${frontendPath}`);
    return;
  }

  const loadOptions = normalizedRoute ? { hash: normalizedRoute } : undefined;
  mainWindow.loadFile(frontendPath, loadOptions).catch((err) => {
    console.error('Failed to load frontend:', err);
  });
}

function handleAppRoute(route) {
  const normalizedRoute = normalizeRoute(route);
  if (!normalizedRoute) {
    return;
  }

  pendingRoute = normalizedRoute;
  if (!mainWindow) {
    return;
  }

  if (mainWindow.isMinimized()) {
    mainWindow.restore();
  }
  mainWindow.focus();
  loadAppRoute(normalizedRoute);
}

function handleDeepLink(url) {
  const route = parseDeepLink(url);
  if (!route) {
    return;
  }

  handleAppRoute(route);
}

app.on('second-instance', (_event, argv) => {
  const route = parseDeepLink(argv);
  if (!route) {
    if (mainWindow) {
      if (mainWindow.isMinimized()) {
        mainWindow.restore();
      }
      mainWindow.focus();
    }
    return;
  }

  handleAppRoute(route);
});

app.on('open-url', (event, url) => {
  event.preventDefault();
  handleDeepLink(url);
});

function getBackendPath() {
  const binaryName = isWindows ? 'backend-server.exe' : 'backend-server';

  // When packaged by electron-builder, extraResources go to process.resourcesPath
  if (app.isPackaged) {
    return path.join(process.resourcesPath, binaryName);
  }

  // In development, look in root directory
  const devPath = path.join(__dirname, binaryName);
  if (fs.existsSync(devPath)) {
    return devPath;
  }

  // Fallback: also check resources directory in development
  return path.join(process.resourcesPath, binaryName);
}

function createWindow() {
  const backendPath = getBackendPath();

  // Ensure data directory exists for SQLite database
  const userDataPath = app.getPath('userData');
  const dataDir = path.join(userDataPath, 'data');
  if (!fs.existsSync(dataDir)) {
    fs.mkdirSync(dataDir, { recursive: true });
  }

  // Set environment variables for the backend
  const env = {
    ...process.env,
    DATA_DIR: dataDir,
    DATABASE_URL: `sqlite+aiosqlite:///${path.join(dataDir, 'towel.db').replace(/\\/g, '/')}`,
    PUBLIC_API_BASE_URL: 'http://localhost:8000',
    CORS_ORIGINS: 'null', // Allow null origin for Electron file:// protocol
  };

  // Check if backend binary exists
  if (!fs.existsSync(backendPath)) {
    console.error('Backend binary not found at:', backendPath);
    app.quit();
    return;
  }

  // Spawn the Go backend in the background
  backendProcess = spawn(backendPath, [], { env, stdio: 'inherit' });

  backendProcess.on('error', (err) => {
    console.error('Failed to start backend:', err);
    dialog.showErrorBox('Backend Error', `Failed to start backend: ${err.message}`);
  });

  backendProcess.on('exit', (code, signal) => {
    if (code !== 0 && code !== null) {
      console.error(`Backend process exited with code ${code}`);
    }
  });

  // 3. Create the browser window
  mainWindow = new BrowserWindow({
    width: 1200,
    height: 800,
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true
    }
  });

  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//i.test(url)) {
      shell.openExternal(url);
      return { action: 'deny' };
    }

    return { action: 'allow' };
  });

  mainWindow.webContents.on('will-navigate', (event, url) => {
    if (/^https?:\/\//i.test(url)) {
      event.preventDefault();
      shell.openExternal(url);
    }
  });

  // 4. Wait a moment for backend to start, then load frontend
  setTimeout(() => {
    loadAppRoute(pendingRoute);
    pendingRoute = '';
  }, 1500);

  mainWindow.on('closed', () => {
    mainWindow = null;
  });
}

if (gotSingleInstanceLock) {
  pendingRoute = parseDeepLink(process.argv);

  app.whenReady().then(() => {
    registerProtocol();
    createWindow();
  });
}

// 5. Cleanup: Kill the Go backend when the Electron app quits
app.on('will-quit', () => {
  if (backendProcess) {
    backendProcess.kill();
  }
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});