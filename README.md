## Lxn-OneDriveCLI

This is an unofficial OneDrive command-line client that supports both Enterprise and Personal (Office 365) editions. This project is a recreation and improvement in GoLang of a closed-source OneDrive tool (OneDrive Uploader) I had been using previously.

中文说明：[Lxn-OneDriveCLI](https://lxnchan.cn/works/onedriveCLI.html)。

### How to install

You can download pre-built binaries from the [release page](https://github.com/LxnChan/OneDrive-Unofficial-CLI/).

Or you can build it yourself by running the following command:

```cmd
git clone https://github.com/LxnChan/OneDrive-Unofficial-CLI.git
cd onedrivecli
build.bat
```

Go Version: `go1.25.3 windows/amd64`. 

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
onedrivecli - OneDrive CLI by LxnChan
https://lxnchan.cn
For more, please visit: https://lxnchan.cn/works/onedriveCLI.html

Usage:
  onedrivecli [--config=<PATH>] [--user-agent=<UA>] [--proxy=<MODE>] [--verbose=<true|false>] <command> [options]

Commands:
  login
  logout
  status
  list
  upload
  download

Parameters:
  Global:
    --config=<PATH>           Config file path. Default: ./config.json next to the executable. On Linux, also tries /etc/odc/config.json
    --user-agent=<UA>         Set User-Agent for all requests and persist to config.json
    --proxy=<MODE>            Proxy: system/none or http(s)://host:port or host:port (will persist to config.json)
    --verbose=<true|false>    Enable verbose output

  login:
    --client-id=<ID>          Azure app (public client) client ID (default: built-in)
    --tenant=<TENANT>         common/consumers/organizations or a specific tenant ID (default: common)
    --scopes=<SCOPES>         Space or comma separated scopes (default: offline_access User.Read Files.ReadWrite.All)

  list:
    --path=<REMOTE_PATH>      Remote directory path (default: root)

  upload:
    --local=<LOCAL_PATH>      Local file or folder path
    --remote=<REMOTE_PATH>    Remote target path
    --chunk-size=<SIZE>       Chunk size (5MiB-60MiB, default: 10MiB)
    --threads=<N>             Threads (default: 2)

  download:
    --remote=<REMOTE_PATH>    Remote file or folder path
    --local=<LOCAL_PATH>      Local target path (optional for folders)
    --chunk-size=<SIZE>       Chunk size (5MiB-60MiB, default: 10MiB)
    --threads=<N>             Threads (default: 2)

Examples:
  onedrivecli --config=./config.json status
  onedrivecli --user-agent="MyApp/1.0" --proxy=system --verbose=true login --client-id=<APP_CLIENT_ID>
  onedrivecli status
  onedrivecli list --path=/Documents
  onedrivecli upload --local=./a.txt --remote=/Documents/a.txt
  onedrivecli download --remote=/Documents/a.txt --local=./a.txt
```

## License

AGPL v3