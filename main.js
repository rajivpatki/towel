const { app, BrowserWindow, dialog } = require('electron');
const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');

let mainWindow;
let backendProcess;

function getBackendPath() {
  const isWin = process.platform === 'win32';
  const binaryName = isWin ? 'backend-server.exe' : 'backend-server';

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

  // 4. Wait a moment for backend to start, then load frontend
  setTimeout(() => {
    const frontendPath = path.join(__dirname, 'frontend', 'dist', 'index.html');
    if (fs.existsSync(frontendPath)) {
      mainWindow.loadFile(frontendPath);
    } else {
      console.error('Frontend not found at:', frontendPath);
      dialog.showErrorBox('Frontend Error', `Frontend not found at: ${frontendPath}`);
    }
  }, 1500);

  mainWindow.on('closed', () => {
    mainWindow = null;
  });
}

app.whenReady().then(createWindow);

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