## Lxn-OneDriveCLI

This is an unofficial OneDrive command-line client that supports both Enterprise and Personal (Office 365) editions. This project is a recreation and improvement in GoLang of a closed-source OneDrive tool (OneDrive Uploader) I had been using previously.

### How to install

You can download pre-built binaries from the [release page](https://github.com/LxnChan/OneDrive-Unofficial-CLI/).

Or you can build it yourself by running the following command:

```cmd
git clone https://github.com/LxnChan/OneDrive-Unofficial-CLI.git
cd onedrivecli
build.bat
```

### Get your own Client ID

I have provided a default ClientID in the source code, but for security and control reasons, it's recommended that you apply for your own ClientID.

1. Go to [Azure Portal](https://portal.azure.com/).
2. Go to `Microsoft Entra ID`
3. Click `Add`->`App Registration`
4. You can input anything in the `Name` box
5. According to your user type, select Single or Multi Tenant, or Any Tenant + Personal Microsoft Account.
6. (Optional) Go to `Manage`->`Authentication (Preview)`->`Settings`, Enable `Allow public client flows` if you need any user to login with this ClientID.
7. Go to `API permissions`, add `email`, `offline_access`, `openid`, `profile`, `user.read` and `Files.ReadWrite.All`.
8. Now back to the `Overview` tab, you can find your `Application (client) ID` there.

### Usage

```cmd
D:\onedriveCLI\onedrivecli\release>onedrivecli-windows-amd64.exe
onedrivecli - OneDrive CLI by LxnChan
https://lxnchan.cn

Usage:
  onedrivecli [--config=<PATH>] [--user-agent=<UA>] [--proxy=<MODE>] [--verbose=<true|false>] <command> [options]

Commands:
  login      Sign in (Device Code flow)
  logout     Sign out (clear local token)
  status     Show account and drive status
  list       List remote directory
  upload     Upload a file or folder
  download   Download a file or folder

Examples:
  onedrivecli --config=./config.json status
  onedrivecli --user-agent="MyApp/1.0" --proxy=system --verbose=true login --client-id=<APP_CLIENT_ID>
  onedrivecli status
  onedrivecli list --path=/Documents
  onedrivecli upload --local=./a.txt --remote=/Documents/a.txt
  onedrivecli download --remote=/Documents/a.txt --local=./a.txt
```