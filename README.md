# Towel

Towel is a self-hosted app with:

- **Frontend** running on `http://localhost:3000`
- **Backend API** running on `http://localhost:8000`
- **Persistent data** stored in `/data` inside the backend container

Your local instance keeps its state in a persistent Docker volume so you can restart containers without losing setup data.

## Prerequisites

If you want the easiest setup, run the installer script for your operating system. It installs Docker if needed, starts Towel with Compose, and opens `http://localhost:3000` in your browser.

If you already have Docker installed, you can skip the installer and just download the Compose file for your OS, then run `docker compose up -d`.

If you want to build Towel from source, clone this repository and use the `docker-compose.yml` file that is already in the repo.

### Windows

Use `install_windows.ps1`.

What it does:

- **Elevates to Administrator** if needed
- **Installs Chocolatey** if missing
- **Enables WSL** and `VirtualMachinePlatform`
- **Installs or updates WSL 2**
- **Installs Docker Desktop** if it is not already present
- **Shows a large warning** if Windows features were enabled and a restart may still be required
- **Starts the Compose stack** for you
- **Opens** `http://localhost:3000` automatically after startup

Run it from PowerShell:

```powershell
.\install_windows.ps1
```

If you already have Docker installed and just want the app, you can run the equivalent manual flow with PowerShell:

```powershell
iwr https://raw.githubusercontent.com/rajivpatki/towel/main/install.yml -OutFile docker-compose.yml; docker compose up -d
```

This is the same pattern the installer uses, except it also tries to launch your browser automatically.

If you want a native Windows installer artifact instead of running the script directly, build the Inno Setup package:

```powershell
.\packaging\windows\build-installer.ps1
```

This generates a Windows setup executable under `packaging\dist\`.

GitHub Actions can also build this installer. The workflow at `.github/workflows/build-installers.yml` uploads the `.exe` as an artifact on every push to `main` and on manual runs.

### Linux

Use `install_linux.sh`.

What it does:

- **Installs Docker Engine** and related Docker CLI plugins
- **Enables and starts** the Docker service
- **Adds your sudo user** to the `docker` group when possible
- **Starts the Compose stack**
- **Opens** `http://localhost:3000` automatically

Run it with:

```bash
chmod +x ./install_linux.sh
./install_linux.sh
```

If you were added to the `docker` group, log out and log back in before using Docker without `sudo`.

If you already have Docker installed, download the Compose file and run it directly:

```bash
curl -L https://raw.githubusercontent.com/rajivpatki/towel/main/install.yml -o docker-compose.yml
docker compose up -d
```

### macOS

Use `install_mac.sh`.

What it does:

- **Installs Homebrew** if missing
- **Installs OrbStack** when available
- **Falls back to Colima + Docker CLI** if OrbStack installation fails
- **Starts the Docker runtime** after installation
- **Starts the Compose stack**
- **Opens** `http://localhost:3000` automatically

Run it with:

```bash
chmod +x ./install_mac.sh
./install_mac.sh
```

If you already have Docker installed, download the Compose file and run it directly:

```bash
curl -L https://raw.githubusercontent.com/rajivpatki/towel/main/install.yml -o docker-compose.yml
docker compose up -d
```

If you want a native macOS installer artifact instead of asking users to run the shell script directly, build the package on a Mac:

```bash
./packaging/macos/build-installer.sh
```

This generates a `.pkg` under `packaging/dist/` that installs `Towel.app` into `/Applications`.

For distribution outside local testing, sign and notarize the macOS app/package with your Apple Developer identities.

GitHub Actions can also build this installer. The workflow at `.github/workflows/build-installers.yml` uploads the `.pkg` as an artifact on every push to `main` and on manual runs.

## Run Towel with Docker Compose

The easiest way to run the application from a cloned repo is with the Compose file that ships in this repository:

```bash
docker compose up --build
```

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
- **Either an AI provider API key or a Google account you want to use for Gemini**

Currently supported AI agents include:

- **OpenAI GPT 5.4** via `openai:gpt-5.4`
- **OpenAI GPT 5.4 Mini** via `openai:gpt-5.4-mini`
- **Google Gemini 3 Flash** via `gemini:gemini-3-flash-preview`
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

In the final setup step, choose an AI model.

You have two setup paths:

- **OpenAI or DeepSeek**
  Choose the model and paste the matching provider API key.
- **Gemini**
  Click `Use Gemini` and Towel will use your connected Google account instead of asking for another API key.

For Gemini, Towel will probe Google access before saving the selection. If the Google Generative Language API is not enabled yet, Towel shows a link to enable it in Google Cloud and then you can retry.

If you are creating a brand new eligible Google Cloud account, Google currently advertises **up to `$300` in free trial credits** for new users. This is a Google Cloud offer, not a Towel offer, and eligibility and duration are determined by Google.

Setup completes only after a valid AI agent is configured successfully.

## Data and persistence

Towel stores its local state in a SQLite database under the container data directory.

Important details:

- **Database path** defaults to `/data/towel.db`
- **Secrets and onboarding state** are stored in the persistent data volume
- **Do not delete your Docker volume** unless you want to reset the app completely

## Developer usage

To build and run locally from a cloned repository:

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
  You may need to restart your machine if the install script enabled WSL or Virtual Machine Platform.

- **Linux Docker permission denied**
  Log out and back in after being added to the `docker` group, or run Docker commands with `sudo`.

- **Google authorization fails**
  Verify your Client ID and Client Secret, and make sure your Gmail address is listed as a test user if the OAuth app is not yet published.

- **Gemini does not activate**
  Confirm that Step 2 completed, approve every requested Google permission, and enable the Google Generative Language API in your Google Cloud project if Towel prompts you to do so.

- **Setup does not finish**
  Confirm that Gmail is connected and that you either entered a valid API key for an API-key model or completed the Gemini activation flow successfully.
