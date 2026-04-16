# Towel

Towel is a self-hosted app with:

- **Frontend** running on `http://localhost:3000`
- **Backend API** running on `http://localhost:8000`
- **Persistent data** stored in `/data` inside the backend container

Your local instance keeps its state in a persistent Docker volume so you can restart containers without losing setup data.

## Prerequisites

Before running Towel, install Docker on your machine using the scripts in `install/` or the equivalent manual steps below.

### Windows

Use `install/windows.ps1`.

What it does:

- **Elevates to Administrator** if needed
- **Installs Chocolatey** if missing
- **Enables WSL** and `VirtualMachinePlatform`
- **Installs or updates WSL 2**
- **Installs Docker Desktop** if it is not already present
- **Prompts for restart** if Windows features were enabled during setup

Run it from PowerShell:

```powershell
.\install\windows.ps1
```

After setup, make sure Docker Desktop is running.

### Linux

Use `install/linux.sh`.

What it does:

- **Installs Docker Engine** and related Docker CLI plugins
- **Enables and starts** the Docker service
- **Adds your sudo user** to the `docker` group when possible

Run it with:

```bash
chmod +x ./install/linux.sh
./install/linux.sh
```

If you were added to the `docker` group, log out and log back in before using Docker without `sudo`.

### macOS

Use `install/mac.sh`.

What it does:

- **Installs Homebrew** if missing
- **Installs OrbStack** when available
- **Falls back to Colima + Docker CLI** if OrbStack installation fails
- **Starts the Docker runtime** after installation

Run it with:

```bash
chmod +x ./install/mac.sh
./install/mac.sh
```

## Run Towel with Docker Compose

The easiest way to run the application is with Docker Compose:

```bash
docker compose up --build
```

This starts:

- **Backend** on port `8000`
- **Frontend** on port `3000`
- **Persistent volume** named `towel_data`

Open the app in your browser:

```text
http://localhost:3000
```

## Run Towel as a Docker image

If you want to build and run manually instead of Compose:

```bash
docker build -t towel .
docker run -p 3000:3000 -p 8000:8000 -v towel_data:/data towel
```

Note:

- **Port `3000`** serves the frontend
- **Port `8000`** serves the backend API
- **Volume `towel_data`** persists the application database and secrets

## First-time application setup

When you open Towel for the first time, the app will guide you through a 3-step onboarding flow.

You will need the following ready:

- **A Google OAuth Desktop App Client ID**
- **A Google OAuth Desktop App Client Secret**
- **A Gmail account** you want to connect
- **An AI provider API key**

Currently supported AI agents include:

- **OpenAI GPT 5.4** via `openai:gpt-5.4`
- **DeepSeek Thinking** via `deepseek:deepseek-thinking`

### Step 1: Create Google OAuth credentials

In the app, Towel will ask for Google OAuth desktop app credentials.

To create them:

- **Configure the OAuth consent screen** at `https://console.cloud.google.com/auth/overview/create`
- **Create an OAuth client** at `https://console.cloud.google.com/auth/clients/create`
- **Choose `Desktop app`** as the application type
- **Copy the Client ID and Client Secret** into Towel

### Step 2: Connect your Gmail account

Towel will redirect you to Google so you can authorize Gmail access.

If your Google OAuth app is still in testing mode, add your Gmail address as a test user first:

- **Open** `https://console.cloud.google.com/apis/credentials/consent`
- **Go to** the `Test users` section
- **Add your Gmail address**
- **Save changes**

Then return to Towel and continue the Gmail connection step.

### Step 3: Configure your AI agent

In the final setup step, choose an AI model and paste the matching provider API key.

Setup completes only after a valid AI agent and API key are saved.

## Data and persistence

Towel stores its local state in a SQLite database under the container data directory.

Important details:

- **Database path** defaults to `/data/towel.db`
- **Secrets and onboarding state** are stored in the persistent data volume
- **Do not delete your Docker volume** unless you want to reset the app completely

## Developer usage

To build and run locally with Compose:

```bash
docker compose up --build
```

To stop the stack:

```bash
docker compose down
```

To rebuild after code changes:

```bash
docker compose up --build
```

## Troubleshooting

- **Docker is not running**
  Start Docker Desktop, OrbStack, or Colima before launching the app.

- **Windows install completed but Docker still does not work**
  Restart your machine if the install script enabled WSL or Virtual Machine Platform.

- **Linux Docker permission denied**
  Log out and back in after being added to the `docker` group, or run Docker commands with `sudo`.

- **Google authorization fails**
  Verify your Client ID and Client Secret, and make sure your Gmail address is listed as a test user if the OAuth app is not yet published.

- **Setup does not finish**
  Confirm that Gmail is connected and that you entered a valid API key for one of the supported AI agents.