// PK95 Monitoring web UI: single page app (vanilla JS), design of PK95 Core.
// Helpers, dialogs, forms and the shell are copied from PK95 ProxBackup – keep them in sync.
"use strict";

const ICONS = {
"home": "M2.25 12l8.954-8.955c.44-.439 1.152-.439 1.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.250c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25",
"apps": "M3.75 6A2.25 2.25 0 016 3.75h2.25A2.25 2.25 0 0110.5 6v2.25a2.25 2.25 0 01-2.25 2.25H6a2.25 2.25 0 01-2.25-2.25V6zM3.75 15.75A2.25 2.25 0 016 13.5h2.25a2.25 2.25 0 012.25 2.25V18a2.25 2.25 0 01-2.25 2.25H6A2.25 2.25 0 013.75 18v-2.25zM13.5 6a2.25 2.25 0 012.25-2.25H18A2.25 2.25 0 0120.25 6v2.25A2.25 2.25 0 0118 10.5h-2.25a2.25 2.25 0 01-2.25-2.25V6zM13.5 15.75a2.25 2.25 0 012.25-2.25H18a2.25 2.25 0 012.25 2.25V18A2.25 2.25 0 0118 20.25h-2.25A2.25 2.25 0 0113.5 18v-2.25z",
"server": "M21.75 17.25v-.228a4.5 4.5 0 00-.12-1.03l-2.268-9.64a3.375 3.375 0 00-3.285-2.602H7.923a3.375 3.375 0 00-3.285 2.602l-2.268 9.64a4.5 4.5 0 00-.12 1.03v.228m19.5 0a3 3 0 01-3 3H5.25a3 3 0 01-3-3m19.5 0a3 3 0 00-3-3H5.25a3 3 0 00-3 3m16.5 0h.008v.008h-.008v-.008zm-3 0h.008v.008h-.008v-.008z",
"rocket": "M15.59 14.37a6 6 0 01-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 006.16-12.12A14.98 14.98 0 009.631 8.41m5.96 5.96a14.926 14.926 0 01-5.841 2.58m-.119-8.54a6 6 0 00-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 00-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 01-2.448-2.448 14.9 14.9 0 01.06-.312m-2.24 2.39a4.493 4.493 0 00-1.757 4.306 4.493 4.493 0 004.306-1.758M16.5 9a1.5 1.5 0 11-3 0 1.5 1.5 0 013 0z",
"cog": "M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.24-.438.613-.431.992a6.759 6.759 0 010 .255c-.007.378.138.75.43.99l1.005.828c.424.35.534.954.26 1.43l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.28c-.09.543-.56.941-1.11.941h-2.594c-.55 0-1.02-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 01-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.992a6.932 6.932 0 010-.255c.007-.378-.138-.75-.43-.99l-1.004-.828a1.125 1.125 0 01-.26-1.43l1.297-2.247a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.087.22-.128.332-.183.582-.495.644-.869l.214-1.281z M15 12a3 3 0 11-6 0 3 3 0 016 0z",
"plus": "M12 4.5v15m7.5-7.5h-15",
"pencil": "M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931zm0 0L19.5 7.125M18 14v4.75A2.25 2.25 0 0115.75 21H5.25A2.25 2.25 0 013 18.75V8.25A2.25 2.25 0 015.25 6H10",
"trash": "M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0",
"play": "M5.25 5.653c0-.856.917-1.398 1.667-.986l11.54 6.348a1.125 1.125 0 010 1.971l-11.54 6.347a1.125 1.125 0 01-1.667-.985V5.653z",
"stop": "M5.25 7.5A2.25 2.25 0 017.5 5.25h9a2.25 2.25 0 012.25 2.25v9a2.25 2.25 0 01-2.25 2.25h-9a2.25 2.25 0 01-2.25-2.25v-9z",
"power": "M5.636 5.636a9 9 0 1012.728 0M12 3v9",
"refresh": "M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0l3.181 3.183a8.25 8.25 0 0013.803-3.7M4.031 9.865a8.25 8.25 0 0113.803-3.7l3.181 3.182m0-4.991v4.99",
"external": "M13.5 6H5.25A2.25 2.25 0 003 8.25v10.5A2.25 2.25 0 005.25 21h10.5A2.25 2.25 0 0018 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25",
"check": "M4.5 12.75l6 6 9-13.5",
"x": "M6 18L18 6M6 6l12 12",
"clock": "M12 6v6h4.5m4.5 0a9 9 0 11-18 0 9 9 0 0118 0z",
"warn": "M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.007v.008H12v-.008z",
"key": "M15.75 5.25a3 3 0 013 3m3 0a6 6 0 01-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1121.75 8.25z",
"globe": "M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0121 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0112 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 013 12c0-1.605.42-3.113 1.157-4.418",
"copy": "M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 01-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 011.5.124m7.5 10.376h3.375c.621 0 1.125-.504 1.125-1.125V11.25c0-4.46-3.243-8.161-7.5-8.876a9.06 9.06 0 00-1.5-.124H9.375c-.621 0-1.125.504-1.125 1.125v3.5m7.5 10.375H9.375a1.125 1.125 0 01-1.125-1.125v-9.25m12 6.625v-1.875a3.375 3.375 0 00-3.375-3.375h-1.5a1.125 1.125 0 01-1.125-1.125v-1.5a3.375 3.375 0 00-3.375-3.375H9.75",
"eye": "M2.036 12.322a1.012 1.012 0 010-.639C3.423 7.51 7.36 4.5 12 4.5c4.638 0 8.573 3.007 9.963 7.178.07.207.07.431 0 .639C20.577 16.49 16.64 19.5 12 19.5c-4.638 0-8.573-3.007-9.963-7.178z M15 12a3 3 0 11-6 0 3 3 0 016 0z",
"eye-off": "M3.98 8.223A10.477 10.477 0 001.934 12C3.226 16.338 7.244 19.5 12 19.5c.993 0 1.953-.138 2.863-.395M6.228 6.228A10.45 10.45 0 0112 4.5c4.756 0 8.773 3.162 10.065 7.498a10.522 10.522 0 01-4.293 5.774M6.228 6.228L3 3m3.228 3.228l3.65 3.65m7.894 7.894L21 21m-3.228-3.228l-3.65-3.65m0 0a3 3 0 10-4.243-4.243m4.242 4.242L9.88 9.88",
"cpu": "M8.25 3v1.5M4.5 8.25H3m18 0h-1.5M4.5 12H3m18 0h-1.5m-15 3.75H3m18 0h-1.5M8.25 19.5V21M12 3v1.5m0 15V21m3.75-18v1.5m0 15V21m-9-1.5h10.5a2.25 2.25 0 002.25-2.25V6.75a2.25 2.25 0 00-2.25-2.25H6.75A2.25 2.25 0 004.5 6.75v10.5a2.25 2.25 0 002.25 2.25zm.75-12h9v9h-9v-9z",
"box": "M21 7.5l-9-5.25L3 7.5m18 0l-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9",
"back": "M15 19l-7-7 7-7",
"lock": "M16.5 10.5V6.75a4.5 4.5 0 10-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 002.25-2.25v-6.75a2.25 2.25 0 00-2.25-2.25H6.75a2.25 2.25 0 00-2.25 2.25v6.75a2.25 2.25 0 002.25 2.25z",
"logout": "M15.75 9V5.25A2.25 2.25 0 0013.5 3h-6a2.25 2.25 0 00-2.25 2.25v13.5A2.25 2.25 0 007.5 21h6a2.25 2.25 0 002.25-2.25V15M12 9l-3 3m0 0l3 3m-3-3h12.75",
"user": "M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.5 20.25a7.5 7.5 0 0115 0",
"sparkles": "M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09z",
"list": "M8.25 6.75h12M8.25 12h12m-12 5.25h12M3.75 6.75h.007v.008H3.75V6.75zm.375 0a.375.375 0 11-.75 0 .375.375 0 01.75 0zM3.75 12h.007v.008H3.75V12zm.375 0a.375.375 0 11-.75 0 .375.375 0 01.75 0zm-.375 5.25h.007v.008H3.75v-.008zm.375 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z",
"link": "M13.19 8.688a4.5 4.5 0 011.242 7.244l-4.5 4.5a4.5 4.5 0 01-6.364-6.364l1.757-1.757m13.35-.622l1.757-1.757a4.5 4.5 0 00-6.364-6.364l-4.5 4.5a4.5 4.5 0 001.242 7.244",
"signal": "M9.348 14.651a3.75 3.75 0 010-5.303m5.304 0a3.75 3.75 0 010 5.303m-7.425 2.122a6.75 6.75 0 010-9.546m9.546 0a6.75 6.75 0 010 9.546M5.106 18.894c-3.808-3.808-3.808-9.98 0-13.789m13.788 0c3.808 3.808 3.808 9.981 0 13.79M12 12h.008v.007H12V12zm.375 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z",
"info": "M11.25 11.25l.041-.02a.75.75 0 011.063.852l-.708 2.836a.75.75 0 001.063.853l.041-.021M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9-3.75h.008v.008H12V8.25z",
"shield": "M9 12.75L11.25 15 15 9.75m-3-7.036A11.959 11.959 0 013.598 6 11.99 11.99 0 003 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285z",
"chevron-down": "M19.5 8.25l-7.5 7.5-7.5-7.5",
"arrow-right": "M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3",
"terminal": "M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0021 18V6a2.25 2.25 0 00-2.25-2.25H5.25A2.25 2.25 0 003 6v12a2.25 2.25 0 002.25 2.25z",
"database": "M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125",
"archive": "M20.25 7.5l-.625 10.632a2.25 2.25 0 01-2.247 2.118H6.622a2.25 2.25 0 01-2.247-2.118L3.75 7.5M10 11.25h4M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z",
"calendar": "M6.75 3v2.25M17.25 3v2.25M3 18.75V7.5a2.25 2.25 0 012.25-2.25h13.5A2.25 2.25 0 0121 7.5v11.25m-18 0A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75m-18 0v-7.5A2.25 2.25 0 015.25 9h13.5A2.25 2.25 0 0121 11.25v7.5",
"folder": "M2.25 12.75V12A2.25 2.25 0 014.5 9.75h15A2.25 2.25 0 0121.75 12v.75m-8.69-6.44l-2.12-2.12a1.5 1.5 0 00-1.061-.44H4.5A2.25 2.25 0 002.25 6v12a2.25 2.25 0 002.25 2.25h15A2.25 2.25 0 0021.75 18V9a2.25 2.25 0 00-2.25-2.25h-5.379a1.5 1.5 0 01-1.06-.44z",
"sync": "M7.5 21L3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5",
"bell": "M14.857 17.082a23.848 23.848 0 005.454-1.31A8.967 8.967 0 0118 9.75v-.7V9A6 6 0 006 9v.75a8.967 8.967 0 01-2.312 6.022c1.733.64 3.56 1.085 5.455 1.31m5.714 0a24.255 24.255 0 01-5.714 0m5.714 0a3 3 0 11-5.714 0",
"download": "M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5M16.5 12L12 16.5m0 0L7.5 12m4.5 4.5V3",
"eject": "M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5m-13.5-9L12 3m0 0l4.5 4.5M12 3v13.5",
"restore": "M9 15L3 9m0 0l6-6M3 9h12a6 6 0 010 12h-3",
"menu": "M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5",
"search": "M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z",
"moon": "M21.752 15.002A9.718 9.718 0 0118 15.75c-5.385 0-9.75-4.365-9.75-9.75 0-1.33.266-2.597.748-3.752A9.753 9.753 0 003 11.25C3 16.635 7.365 21 12.75 21a9.753 9.753 0 009.002-5.998z",
"sun": "M12 3v2.25m6.364.386l-1.591 1.591M21 12h-2.25m-.386 6.364l-1.591-1.591M12 18.75V21m-4.773-4.227l-1.591 1.591M5.25 12H3m4.227-4.773L5.636 5.636M15.75 12a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0z",
"usb": "M8.25 3v1.5M4.5 8.25H3m18 0h-1.5M4.5 12H3m18 0h-1.5m-15 3.75H3m18 0h-1.5M8.25 19.5V21M12 3v1.5m0 15V21m3.75-18v1.5m0 15V21m-9-1.5h10.5a2.25 2.25 0 002.25-2.25V6.75a2.25 2.25 0 00-2.25-2.25H6.75A2.25 2.25 0 004.5 6.75v10.5a2.25 2.25 0 002.25 2.25zm.75-12h9v9h-9v-9z",
"disk": "M5.25 14.25h13.5m-13.5 0a3 3 0 01-3-3m3 3a3 3 0 100 6h13.5a3 3 0 100-6m-16.5-3a3 3 0 013-3h13.5a3 3 0 013 3m-19.5 0a4.5 4.5 0 01.9-2.7L5.737 5.1a3.375 3.375 0 012.7-1.35h7.126c1.062 0 2.062.5 2.7 1.35l2.587 3.45a4.5 4.5 0 01.9 2.7m0 0a3 3 0 01-3 3m0 3h.008v.008h-.008v-.008zm0-6h.008v.008h-.008v-.008zm-3 6h.008v.008h-.008v-.008zm0-6h.008v.008h-.008v-.008z",
"desktop": "M9 17.25v1.007a3 3 0 01-.879 2.122L7.5 21h9l-.621-.621A3 3 0 0115 18.257V17.25m6-12V15a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 15V5.25m18 0A2.25 2.25 0 0018.75 3H5.25A2.25 2.25 0 003 5.25m18 0V12a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 12V5.25",
"chart": "M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 013 19.875v-6.75zM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V8.625zM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V4.125z",
"wrench": "M21.75 6.75a4.5 4.5 0 01-4.884 4.484c-1.076-.091-2.264.071-2.95.904l-7.152 8.684a2.548 2.548 0 11-3.586-3.586l8.684-7.152c.833-.686.995-1.874.904-2.95a4.5 4.5 0 016.336-4.486l-3.276 3.276a3.004 3.004 0 002.25 2.25l3.276-3.276c.256.565.398 1.192.398 1.852z",
"bolt": "M3.75 13.5l10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75z",
"printer": "M6.72 13.829c-.24.03-.48.062-.72.096m.72-.096a42.415 42.415 0 0110.56 0m-10.56 0L6.34 18m10.94-4.171c.24.03.48.062.72.096m-.72-.096L17.66 18m0 0l.229 2.523a1.125 1.125 0 01-1.12 1.227H7.231c-.662 0-1.18-.568-1.12-1.227L6.34 18m11.318 0h1.091A2.25 2.25 0 0021 15.75V9.456c0-1.081-.768-2.015-1.837-2.175a48.055 48.055 0 00-1.913-.247M6.34 18H5.25A2.25 2.25 0 013 15.75V9.456c0-1.081.768-2.015 1.837-2.175a48.041 48.041 0 011.913-.247m10.5 0a48.536 48.536 0 00-10.5 0m10.5 0V3.375c0-.621-.504-1.125-1.125-1.125h-8.25c-.621 0-1.125.504-1.125 1.125v3.659M18 10.5h.008v.008H18V10.5zm-3 0h.008v.008H15V10.5z",
"pulse": "M3 12h4l3-8 4 16 3-8h4",
};

// ---------- helpers ----------
const app = document.getElementById("app");
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const icon = (name, cls = "") => `<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true"><path d="${ICONS[name] || ICONS.box}"/></svg>`;
const qs = (sel, root = document) => root.querySelector(sel);
const qsa = (sel, root = document) => [...root.querySelectorAll(sel)];
const enc = encodeURIComponent;
const NNBSP = " "; // narrow no-break space between a number and its unit
const nowSec = () => Math.floor(Date.now() / 1000);
const isNum = (v) => v !== null && v !== undefined && v !== "" && isFinite(v);
const int = (v) => (isFinite(v) ? Math.round(Number(v)) : 0); // API counts, safe to interpolate

function fmtBytes(n) {
  if (!n || !isFinite(n)) return "0 B";
  const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)).replace(".", ",") + " " + u[i];
}
function fmtTime(ts, withYear) {
  if (!ts) return "—";
  const d = new Date(ts * 1000);
  const p = (x) => String(x).padStart(2, "0");
  return `${p(d.getDate())}.${p(d.getMonth() + 1)}.${withYear ? d.getFullYear() : ""} ${p(d.getHours())}:${p(d.getMinutes())}`; // "11.11. 21:09", not "11.11.21:09"
}
function fmtAgo(ts) {
  if (!ts) return "nie";
  const s = Math.round(Date.now() / 1000 - ts);
  if (s < 60) return "gerade eben";
  if (s < 3600) return `vor ${Math.round(s / 60)} Min.`;
  if (s < 86400) return `vor ${Math.round(s / 3600)} Std.`;
  const d = Math.round(s / 86400);
  return d === 1 ? "gestern" : `vor ${d} Tagen`;
}
// fmtSince: "seit 5 Min." for state changes and open alerts.
function fmtSince(ts) {
  if (!ts) return "";
  const s = Math.max(0, Math.round(Date.now() / 1000 - ts));
  if (s < 60) return "seit weniger als 1 Min.";
  if (s < 3600) return `seit ${Math.floor(s / 60)} Min.`;
  if (s < 86400) return `seit ${Math.floor(s / 3600)} Std.`;
  const d = Math.floor(s / 86400);
  return `seit ${d} ${d === 1 ? "Tag" : "Tagen"}`;
}
function fmtDur(a, b) {
  if (!a || !b) return "";
  const s = b - a;
  if (s < 60) return s + " s";
  if (s < 3600) return Math.round(s / 60) + " Min.";
  return (s / 3600).toFixed(1).replace(".", ",") + " Std.";
}
// fmtUptime: seconds as "3 Tage 4 Std." / "5 Std. 12 Min." / "7 Min.".
function fmtUptime(s) {
  s = Math.max(0, Math.round(s || 0));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return `${d.toLocaleString("de-DE")} ${d === 1 ? "Tag" : "Tage"}${h ? ` ${h} Std.` : ""}`;
  if (h) return `${h} Std.${m ? ` ${m} Min.` : ""}`;
  if (m) return `${m} Min.`;
  return `${s} s`;
}
function fmtNum(v, max = 1) {
  if (!isNum(v)) return "—";
  return Number(v).toLocaleString("de-DE", { maximumFractionDigits: max });
}
const fmtPct = (v) => (isNum(v) ? fmtNum(v, Math.abs(v) < 10 ? 1 : 0) + NNBSP + "%" : "—");
// fmtVal formats a metric value by its unit (see DESIGN.md "Metrics").
function fmtVal(v, unit) {
  if (!isNum(v)) return "—";
  v = Number(v);
  switch (unit) {
    case "%": return fmtPct(v);
    case "B": return fmtBytes(v);
    case "B/s": return fmtBytes(v) + "/s";
    case "ms": return fmtNum(v, v < 10 ? 1 : 0) + NNBSP + "ms";
    case "s": return fmtUptime(v);
    case "d": { const n = Math.floor(v); return `${n.toLocaleString("de-DE")} ${n === 1 ? "Tag" : "Tage"}`; }
    case "min": return fmtNum(v, 0) + NNBSP + "Min.";
    case "1/s": return fmtNum(v, 2) + "/s";
    case "rpm": return fmtNum(v, 0) + NNBSP + "U/min";
    case "":
    case undefined:
    case null: return fmtNum(v, 2);
    default: return fmtNum(v, 1) + NNBSP + unit;
  }
}
function fmtSpeed(bps) {
  if (!bps) return "";
  if (bps >= 1e9) return fmtNum(bps / 1e9, 1) + " Gbit/s";
  if (bps >= 1e6) return fmtNum(bps / 1e6, 0) + " Mbit/s";
  return fmtNum(bps / 1e3, 0) + " kbit/s";
}
function pct(used, total) { return total ? Math.round((used * 100) / total) : 0; }
function usageBar(used, total) {
  if (!total) return `<div class="usage"><span class="track"></span><span class="pct">—</span></div>`;
  const p = pct(used, total);
  const cls = p >= 90 ? "bad" : p >= 75 ? "warn" : "";
  return `<div class="usage" title="${fmtBytes(used)} von ${fmtBytes(total)}"><span class="track"><span class="fill ${cls}" style="width:${p}%"></span></span><span class="pct">${p} %</span></div>`;
}
// pctBar draws a usage bar from a percentage (high = bad); levelBar the other way round (battery, toner).
// With a check state the colour follows that state (the configured thresholds) instead of the fixed limits.
function pctBar(p, title = "", low = false, st = "") {
  if (!isNum(p)) return `<div class="usage"><span class="track"></span><span class="pct">—</span></div>`;
  const w = Math.max(0, Math.min(100, p));
  const cls = st ? ({ crit: "bad", warn: "warn" }[st] || "") : low ? (p <= 10 ? "bad" : p <= 25 ? "warn" : "") : (p >= 90 ? "bad" : p >= 75 ? "warn" : "");
  return `<div class="usage"${title ? ` title="${esc(title)}"` : ""}><span class="track"><span class="fill ${cls}" style="width:${w.toFixed(1)}%"></span></span><span class="pct">${Math.round(p)}${NNBSP}%</span></div>`;
}
const levelBar = (p, title, st) => pctBar(p, title, true, st);
const pill = (text, tone = "", dot = false, pulse = false) => `<span class="pill ${tone} ${pulse ? "pulse" : ""}">${dot ? '<span class="d"></span>' : ""}${esc(text)}</span>`;
function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } }
// rid returns a random 6-character id for new network checks.
function rid() {
  const a = "abcdefghijklmnopqrstuvwxyz0123456789", b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return [...b].map((x) => a[x % a.length]).join("");
}
function groupBy(list, keyFn) {
  const m = new Map();
  for (const x of list) { const k = keyFn(x); if (!m.has(k)) m.set(k, []); m.get(k).push(x); }
  return [...m.entries()];
}
const byName = (a, b) => String(a.name || "").localeCompare(String(b.name || ""), "de", { numeric: true });

// ---------- labels ----------
const TYPE_LABELS = { proxmox: "Proxmox VE", agent: "Agent", snmp: "SNMP", nut: "USV (NUT)", net: "Netzwerk" };
const TYPE_ICONS = { proxmox: "server", agent: "desktop", snmp: "signal", nut: "bolt", net: "globe" };
const STATES = { ok: ["OK", "green"], warn: ["Warnung", "amber"], crit: ["Kritisch", "rose"], unknown: ["Unbekannt", ""], maintenance: ["Wartung", "blue"], disabled: ["Deaktiviert", ""] };
const SDOT = { ok: "ok", warn: "warning", crit: "error", unknown: "", maintenance: "blue", disabled: "" };
const RANK = { ok: 0, disabled: 0, maintenance: 0, unknown: 1, warn: 2, crit: 3 };
// attTag: badge of a device with attached agents (DeviceRow.attached) in lists.
const attTag = (d) => (int(d.attached) > 0 ? ` <span class="tag att-tag" title="Zugeordnete Agents ergänzen dieses Gerät">+${int(d.attached)} ${int(d.attached) === 1 ? "Agent" : "Agents"}</span>` : "");
const statePill = (s, dot = true) => { const [l, t] = STATES[s] || STATES.unknown; return pill(l, t, dot); };
const sevPill = (s) => (s === "crit" ? pill("Kritisch", "rose") : pill("Warnung", "amber"));
const OS_LABELS = { linux: "Linux", windows: "Windows", darwin: "macOS" };
const ARCH_LABELS = { amd64: "x86-64 (amd64)", arm64: "ARM64", "386": "x86 (32 Bit)", arm: "ARM (32 Bit)" };

// devState is the state a device row shows: deactivated and maintenance win over the check state.
function devState(d) {
  if (d.enabled === false) return "disabled";
  const mu = d.maintenance_until ?? (d.maintenance ? d.maintenance.until : 0);
  if (mu && mu > nowSec()) return "maintenance";
  return ["ok", "warn", "crit", "unknown"].includes(d.state) ? d.state : "unknown";
}

// METRIC_LABELS: German label and unit of every metric name in DESIGN.md.
const METRIC_LABELS = {
  "cpu.usage": ["CPU-Auslastung", "%"],
  "cpu.load1": ["Systemlast (1 Min.)", ""],
  "mem.usage": ["Arbeitsspeicher", "%"],
  "mem.used": ["Arbeitsspeicher belegt", "B"],
  "mem.total": ["Arbeitsspeicher gesamt", "B"],
  "swap.usage": ["Auslagerung", "%"],
  "disk.usage": ["Festplattenbelegung", "%"],
  "disk.used": ["Festplatte belegt", "B"],
  "disk.total": ["Festplatte gesamt", "B"],
  "net.rx": ["Netzwerk empfangen", "B/s"],
  "net.tx": ["Netzwerk gesendet", "B/s"],
  "uptime": ["Laufzeit", "s"],
  "procs": ["Prozesse", ""],
  "services.failed": ["Ausgefallene Dienste", ""],
  "updates.pending": ["Ausstehende Updates", ""],
  "updates.security": ["Sicherheitsupdates", ""],
  "eventlog.errors": ["Fehler im Ereignisprotokoll (24 Std.)", ""],
  "guest.cpu": ["Gast-CPU", "%"],
  "guest.mem": ["Gast-Arbeitsspeicher", "%"],
  "guest.mem_used": ["Gast-Arbeitsspeicher belegt", "B"],
  "guest.netin": ["Gast-Netzwerk empfangen", "B/s"],
  "guest.netout": ["Gast-Netzwerk gesendet", "B/s"],
  "guest.diskread": ["Gast-Datenträger lesen", "B/s"],
  "guest.diskwrite": ["Gast-Datenträger schreiben", "B/s"],
  "storage.usage": ["Speicherbelegung", "%"],
  "storage.used": ["Speicher belegt", "B"],
  "storage.total": ["Speicher gesamt", "B"],
  "ping.rtt": ["Ping-Laufzeit", "ms"],
  "ping.loss": ["Paketverlust", "%"],
  "http.time": ["Antwortzeit", "ms"],
  "http.status": ["HTTP-Status", ""],
  "cert.days": ["Zertifikat gültig (Tage)", "d"],
  "tcp.time": ["TCP-Verbindungszeit", "ms"],
  "dns.time": ["DNS-Antwortzeit", "ms"],
  "if.rx": ["Port empfangen", "B/s"],
  "if.tx": ["Port gesendet", "B/s"],
  "if.errors": ["Port-Fehler", "1/s"],
  "ups.battery": ["Akku", "%"],
  "ups.runtime": ["Restlaufzeit", "min"],
  "ups.load": ["USV-Last", "%"],
  "ups.input": ["Eingangsspannung", "V"],
  "ups.output": ["Ausgangsspannung", "V"],
  "ups.power": ["USV-Leistung", "W"],
  "ups.battery_voltage": ["Akkuspannung", "V"],
  "poe.power": ["PoE-Verbrauch", "W"],
  "poe.budget": ["PoE-Budget", "W"],
  "poe.usage": ["PoE-Auslastung", "%"],
  "poe.port": ["PoE-Leistung", "W"],
  "fan.rpm": ["Lüfter", "rpm"],
  "temp": ["Temperatur", "°C"],
  "printer.toner": ["Toner", "%"],
  "printer.pages": ["Gedruckte Seiten", ""],
  "custom": ["Eigener Wert", ""],
};
const metricLabel = (n) => (METRIC_LABELS[n] || [n])[0];
const metricUnitOf = (n) => (METRIC_LABELS[n] || [])[1] || "";
// Metric JSON: {n: name, i: instance, v: value, u: unit}; an empty unit is omitted.
const mUnit = (m) => m.u || metricUnitOf(m.n);
function mval(c, name, inst) {
  const m = (c.metrics || []).find((x) => x.n === name && (inst === undefined || (x.i || "") === inst));
  return m ? m.v : null;
}
function checksSummary(c) {
  if (!c) return '<span class="muted">—</span>';
  const parts = [];
  const n = { ok: int(c.ok), warn: int(c.warn), crit: int(c.crit), unknown: int(c.unknown) };
  if (n.crit) parts.push(`<span class="cs-crit">${n.crit} Kritisch</span>`);
  if (n.warn) parts.push(`<span class="cs-warn">${n.warn} ${n.warn === 1 ? "Warnung" : "Warnungen"}</span>`);
  if (n.unknown) parts.push(`<span class="cs-unk">${n.unknown} Unbekannt</span>`);
  parts.push(`<span class="cs-ok">${n.ok} OK</span>`);
  return parts.join(" · ");
}

// ---------- API ----------
async function api(method, path, body) {
  const opt = { method, headers: { "X-PK95": "1" }, credentials: "same-origin" };
  if (body !== undefined) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
  let res;
  try { res = await fetch(path, opt); } catch (e) { throw Object.assign(new Error("Server nicht erreichbar."), { network: true }); }
  let data = {};
  try { data = await res.json(); } catch (e) { /* empty */ }
  if (res.status === 401 && !path.startsWith("/api/login") && !path.startsWith("/api/setup") && !path.startsWith("/api/account")) {
    state.user = null; render(); throw new Error(data.error || "Bitte anmelden.");
  }
  if (!res.ok) throw Object.assign(new Error(data.error || `Fehler ${res.status}`), { fields: data.fields || null, status: res.status });
  return data;
}
const GET = (p) => api("GET", p);
const POST = (p, b) => api("POST", p, b ?? {});
const PUT = (p, b) => api("PUT", p, b ?? {});
const DEL = (p) => api("DELETE", p);

// ---------- toasts ----------
function toast(text, kind = "ok") {
  const box = document.getElementById("toasts");
  const el = document.createElement("div");
  el.className = "toast " + (kind === "error" ? "error" : "");
  el.setAttribute("role", kind === "error" ? "alert" : "status");
  el.innerHTML = `<span class="tic">${icon(kind === "error" ? "warn" : "check")}</span><span class="tt">${esc(text)}</span><button type="button" aria-label="Schließen">${icon("x")}</button>`;
  el.querySelector("button").onclick = () => el.remove();
  box.appendChild(el);
  if (kind !== "error") setTimeout(() => el.remove(), 4000);
}
const toastErr = (e) => toast(e.message || String(e), "error");

// ---------- dialogs ----------
function openDialog(html, cls = "") {
  const d = document.createElement("dialog");
  d.className = cls;
  d.innerHTML = html;
  document.body.appendChild(d);
  d.addEventListener("close", () => setTimeout(() => d.remove(), 0));
  d.addEventListener("click", (e) => { if (e.target === d && !d.dataset.busy) d.close(); });
  d.showModal();
  return d;
}

function confirmDialog({ title, text = "", button = "Löschen", tone = "red", ic }) {
  return new Promise((resolve) => {
    const d = openDialog(`
      <div class="dlg-head"><span class="dlg-ic ${tone === "red" ? "red" : ""}">${icon(ic || (tone === "red" ? "trash" : "refresh"))}</span>
        <div class="grow"><div class="dlg-title">${esc(title)}</div>${text ? `<p class="dlg-text">${text}</p>` : ""}</div></div>
      <div class="dlg-foot" style="margin-top:20px"><button class="btn gray" data-r="0" type="button" autofocus>Abbrechen</button><button class="btn ${tone === "red" ? "red" : ""}" data-r="1" type="button">${esc(button)}</button></div>`);
    let result = false;
    qsa("[data-r]", d).forEach((b) => b.onclick = () => { result = b.dataset.r === "1"; d.close(); });
    d.addEventListener("close", () => resolve(result));
    qs('[data-r="0"]', d).focus();
  });
}

// field(spec) renders one form field. spec: {name, label, type, value, hint, options, required, placeholder, wide, mono, list, step, min, max}
function field(f) {
  const id = "f-" + f.name;
  const req = f.required ? '<span class="req" aria-hidden="true">*</span>' : "";
  const ph = f.placeholder ? ` placeholder="${esc(f.placeholder)}"` : "";
  const cls = f.mono ? ' class="mono"' : "";
  let input = "";
  switch (f.type) {
    case "select":
      input = `<select id="${id}" name="${f.name}">${(f.options || []).map((o) => {
        const [v, l, dis] = Array.isArray(o) ? o : [o, o];
        return `<option value="${esc(v)}" ${String(v) === String(f.value ?? "") ? "selected" : ""} ${dis ? "disabled" : ""}>${esc(l)}</option>`;
      }).join("")}</select>`;
      break;
    case "checkbox":
      return `<div class="field ${f.wide ? "wide" : ""}" data-field="${f.name}"><label class="check"><input type="checkbox" id="${id}" name="${f.name}" ${f.value ? "checked" : ""}><span class="ct"><span>${esc(f.label)}</span>${f.hint ? `<small>${esc(f.hint)}</small>` : ""}</span></label><span class="err" hidden></span></div>`;
    case "radio":
      return `<div class="field wide" data-field="${f.name}">${f.label ? `<span class="fl">${esc(f.label)}${req}</span>` : ""}<div class="radio-cards">${(f.options || []).map(([v, l, h]) =>
        `<label class="radio-card"><input type="radio" name="${f.name}" value="${esc(v)}" ${String(v) === String(f.value) ? "checked" : ""}><span><b>${esc(l)}</b>${h ? `<small>${esc(h)}</small>` : ""}</span></label>`).join("")}</div><span class="err" hidden></span>${f.hint ? `<span class="hint">${esc(f.hint)}</span>` : ""}</div>`;
    case "textarea":
      input = `<textarea id="${id}" name="${f.name}"${ph}${cls} rows="${f.rows || 3}">${esc(f.value)}</textarea>`;
      break;
    case "html":
      return `<div class="field ${f.wide ? "wide" : ""}" data-field="${f.name || ""}">${f.html}</div>`;
    default:
      input = `<input type="${f.type || "text"}" id="${id}" name="${f.name}" value="${esc(f.value)}"${ph}${cls} ${f.autocomplete ? `autocomplete="${f.autocomplete}"` : 'autocomplete="off"'} ${f.min !== undefined ? `min="${f.min}"` : ""} ${f.max !== undefined ? `max="${f.max}"` : ""} ${f.step !== undefined ? `step="${f.step}"` : ""} ${f.list ? `list="${f.list}"` : ""} spellcheck="false">`;
  }
  if (f.button) input = `<div class="inline">${input}<button type="button" class="btn gray" data-fbtn="${f.name}">${esc(f.button)}</button></div>`;
  return `<div class="field ${f.wide ? "wide" : ""}" data-field="${f.name}"><label class="fl" for="${id}">${esc(f.label)}${req}</label>${input}${f.hint ? `<span class="hint">${f.hintHTML ? f.hint : esc(f.hint)}</span>` : ""}<span class="err" hidden></span></div>`;
}

function formValues(root) {
  const v = {};
  qsa("input, select, textarea", root).forEach((el) => {
    if (!el.name) return;
    if (el.type === "checkbox") v[el.name] = el.checked;
    else if (el.type === "radio") { if (el.checked) v[el.name] = el.value; }
    else if (el.type === "number") v[el.name] = el.value === "" ? 0 : Number(el.value);
    else v[el.name] = el.value;
  });
  return v;
}

function showErrors(root, err) {
  qsa(".err", root).forEach((e) => { e.hidden = true; e.textContent = ""; });
  qsa("[aria-invalid]", root).forEach((e) => e.removeAttribute("aria-invalid"));
  const box = qs(".form-error", root);
  if (box) { box.hidden = true; box.textContent = ""; }
  if (!err) return;
  let shown = false;
  if (err.fields) {
    for (const [k, msg] of Object.entries(err.fields)) {
      const f = qs(`[data-field="${CSS.escape(k)}"]`, root);
      if (!f) continue;
      const e = qs(".err", f);
      if (!e) continue;
      e.textContent = msg; e.hidden = false; shown = true;
      const inp = qs("input, select, textarea", f);
      if (inp) inp.setAttribute("aria-invalid", "true");
    }
  }
  if (!shown || !err.fields) { if (box) { box.textContent = err.message; box.hidden = false; } else toastErr(err); }
  else if (box && Object.keys(err.fields).some((k) => !qs(`[data-field="${CSS.escape(k)}"]`, root))) { box.textContent = Object.values(err.fields).join(" "); box.hidden = false; }
  const first = qs('[aria-invalid="true"]', root);
  if (first) { const sec = first.closest("details"); if (sec) sec.open = true; }
}
// stripErr removes the section prefix ("notify.", "general." …) so errors of PUT /api/settings
// land at the plain field names of the section forms.
function stripErr(err, prefix) {
  if (!err || !err.fields) return err;
  const f = {};
  for (const [k, v] of Object.entries(err.fields)) f[k.startsWith(prefix + ".") ? k.slice(prefix.length + 1) : k] = v;
  return Object.assign(new Error(err.message), { fields: f, status: err.status });
}

// formDialog opens a dialog with fields; submit(values, dialog) may throw (field errors are shown).
function formDialog({ title, text, ic = "pencil", tone = "", fields, submit, button = "Speichern", cls = "wide", extra = "", onMount, extraButtons = "" }) {
  const d = openDialog(`
    <form method="dialog" novalidate>
      <div class="dlg-head"><span class="dlg-ic ${tone}">${icon(ic)}</span><div class="grow"><div class="dlg-title">${esc(title)}</div>${text ? `<p class="dlg-text">${text}</p>` : ""}</div></div>
      <div class="dlg-body"><div class="form-error" hidden></div><div class="form-grid">${fields.map(field).join("")}</div>${extra}</div>
      <div class="dlg-foot">${extraButtons}<span class="spacer"></span><button class="btn gray" type="button" data-cancel>Abbrechen</button><button class="btn ${tone === "red" ? "red" : ""}" type="submit">${esc(button)}</button></div>
    </form>`, cls);
  const form = qs("form", d);
  qs("[data-cancel]", d).onclick = () => d.close();
  form.onsubmit = async (e) => {
    e.preventDefault();
    const btn = qs('button[type="submit"]', d);
    btn.disabled = true; d.dataset.busy = "1";
    const old = btn.innerHTML;
    btn.innerHTML = `<span class="spinner"></span> ${esc(button)}`;
    try {
      showErrors(d, null);
      const r = await submit(formValues(form), d);
      if (r !== false) d.close();
    } catch (err) { showErrors(d, err); }
    finally { btn.disabled = false; btn.innerHTML = old; delete d.dataset.busy; }
  };
  const first = qs("input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea", d);
  if (first) first.focus();
  if (onMount) onMount(d);
  return d;
}

async function run(btn, fn, okText) {
  if (btn) { btn.disabled = true; btn.dataset.html = btn.innerHTML; btn.innerHTML = `<span class="spinner"></span>` + (btn.classList.contains("ib") ? "" : " " + btn.textContent.trim()); }
  try { const r = await fn(); if (okText) toast(okText); return r; }
  catch (e) { toastErr(e); }
  finally { if (btn && btn.isConnected) { btn.disabled = false; btn.innerHTML = btn.dataset.html; } }
}

function codeBlock(text) {
  return `<div class="code"><pre>${esc(text)}</pre><button class="ib" type="button" title="Kopieren" aria-label="Kopieren" data-copy="${esc(text)}">${icon("copy")}</button></div>`;
}
function bindCopy(root) {
  qsa("[data-copy]", root).forEach((b) => b.onclick = async () => {
    try { await navigator.clipboard.writeText(b.dataset.copy); toast("Kopiert."); }
    catch (e) { toast("Kopieren nicht möglich – bitte markieren und kopieren.", "error"); }
  });
}
const loadingPanel = () => `<div class="panel flex"><span class="spinner"></span> Lade …</div>`;

// ---------- state, router ----------
const state = { user: null, setup: false, version: "", instance: "", ov: null, ovAt: 0, groups: [] };
let pageTimer = null;
let pageToken = 0;
let onResize = null; // set by pages that draw size-dependent content (charts)

const NAV = [
  { path: "/", label: "Übersicht", icon: "home" },
  { head: "Überwachung" },
  { path: "/devices", label: "Geräte", icon: "server" },
  { path: "/alerts", label: "Alarme", icon: "bell" },
  { path: "/events", label: "Ereignisse", icon: "list" },
  { head: "Einrichtung" },
  { path: "/agents", label: "Agents", icon: "download" },
  { path: "/settings", label: "Einstellungen", icon: "cog" },
];

function go(path) {
  if (location.pathname + location.search + location.hash !== path) history.pushState({}, "", path);
  document.body.classList.remove("menu-open");
  render();
  window.scrollTo(0, 0);
}
window.addEventListener("popstate", render);
document.addEventListener("click", (e) => {
  const a = e.target.closest("a[data-go]");
  if (a && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) { e.preventDefault(); go(a.getAttribute("href")); }
});
let resizeTimer = null;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => { if (onResize) onResize(); }, 150); });

function setTheme() {
  const root = document.documentElement;
  const dark = root.getAttribute("data-theme") ? root.getAttribute("data-theme") === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
  const next = dark ? "light" : "dark";
  root.setAttribute("data-theme", next);
  lsSet("pk95mon-theme", next);
  qsa("[data-theme-icon]").forEach((el) => el.innerHTML = themeIcon());
}
function themeIcon() {
  const root = document.documentElement;
  const dark = root.getAttribute("data-theme") ? root.getAttribute("data-theme") === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
  return icon(dark ? "sun" : "moon");
}

function shell() {
  const cur = location.pathname;
  const collapsed = lsGet("pk95mon-menu") === "collapsed";
  document.body.classList.toggle("collapsed", collapsed);
  app.innerHTML = `
    <nav class="topbar">
      <div class="left">
        <button class="bar-btn mobile-only" type="button" data-menu aria-label="Seitenleiste öffnen">${icon("menu")}</button>
        <button class="bar-btn desk-only" type="button" data-collapse aria-label="Menü ein- oder ausklappen" title="Menü ein- oder ausklappen">${icon("menu")}</button>
        <a href="/" data-go class="brand"><img src="/logo.svg" alt=""><span>PK95 Monitoring</span></a>
        <span class="tag inst-tag" data-inst ${state.instance ? "" : "hidden"}>${esc(state.instance)}</span>
      </div>
      <div class="right">
        <a class="bar-btn alert-ind" href="/alerts" data-go data-alert-ind title="Aktive Alarme" aria-label="Aktive Alarme">${icon("bell")}<span class="alert-count" hidden></span></a>
        <button class="bar-btn" type="button" data-theme-icon title="Zwischen hellem und dunklem Erscheinungsbild wechseln" aria-label="Erscheinungsbild wechseln">${themeIcon()}</button>
        <span class="user-chip"><span class="avatar">${esc((state.user || "?").slice(0, 2))}</span><span class="uname">${esc(state.user)}</span></span>
        <button class="bar-btn" type="button" data-logout title="Abmelden" aria-label="Abmelden">${icon("logout")}</button>
      </div>
    </nav>
    <div class="scrim" data-scrim></div>
    <aside class="sidebar" aria-label="Seitenleiste">
      <ul class="nav-list">
        ${NAV.map((n) => n.head ? `<li class="nav-head">${esc(n.head)}</li>` :
          `<li><a class="nav-link" href="${n.path}" data-go ${cur === n.path || (n.path !== "/" && cur.startsWith(n.path)) ? 'aria-current="page"' : ""}>${icon(n.icon)}<span>${esc(n.label)}</span></a></li>`).join("")}
      </ul>
      <a href="/settings" data-go class="side-foot" style="text-decoration:none">PK95 Monitoring v${esc(state.version)}</a>
    </aside>
    <main><div class="wrap" id="page"></div></main>`;
  qs("[data-menu]").onclick = () => document.body.classList.toggle("menu-open");
  qs("[data-scrim]").onclick = () => document.body.classList.remove("menu-open");
  qs("[data-collapse]").onclick = () => {
    const c = !document.body.classList.contains("collapsed");
    document.body.classList.toggle("collapsed", c);
    lsSet("pk95mon-menu", c ? "collapsed" : "open");
  };
  qs("[data-theme-icon]").onclick = setTheme;
  qs("[data-logout]").onclick = async () => { await POST("/api/logout").catch(() => {}); state.user = null; state.ov = null; render(); };
  drawAlertInd();
  pollOverview();
}

function setInstance(name) {
  state.instance = name || "";
  const t = qs("[data-inst]");
  if (t) { t.textContent = state.instance; t.hidden = !state.instance; }
  document.title = state.instance ? `${state.instance} – PK95 Monitoring` : "PK95 Monitoring";
}

// ---------- alert indicator (top bar) ----------
// The overview is polled every 15 s while logged in; the Übersicht page shares the result.
let ovTimer = null;
async function loadOverview() {
  const r = await GET("/api/overview");
  state.ov = r; state.ovAt = Date.now();
  if (r.groups) state.groups = r.groups;
  drawAlertInd();
  return r;
}
async function pollOverview() {
  clearTimeout(ovTimer);
  if (!state.user || !qs("[data-alert-ind]")) return;
  if (Date.now() - state.ovAt > 10000) { try { await loadOverview(); } catch (e) { /* keep last state */ } }
  ovTimer = setTimeout(pollOverview, 15000);
}
function drawAlertInd() {
  const a = qs("[data-alert-ind]");
  if (!a) return;
  const list = (state.ov && state.ov.alerts) || [];
  const n = list.length, crit = list.filter((x) => x.severity === "crit").length;
  a.classList.toggle("crit", crit > 0);
  a.classList.toggle("warn", !crit && n > 0);
  const c = qs(".alert-count", a);
  c.hidden = !n; c.textContent = n > 99 ? "99+" : String(n);
  const t = n ? `${n === 1 ? "1 aktiver Alarm" : n + " aktive Alarme"}${crit ? `, davon ${crit} kritisch` : ""}` : "Keine aktiven Alarme";
  a.title = t; a.setAttribute("aria-label", t);
}

function pageHead(title, sub = "", actions = "", back = null) {
  return `<div class="page-head"><div style="min-width:0">${back ? `<a class="back" href="${back[0]}" data-go>${icon("back")}${esc(back[1])}</a>` : ""}<h1>${title}</h1>${sub ? `<div class="sub">${sub}</div>` : ""}</div>${actions ? `<div class="actions">${actions}</div>` : ""}</div>`;
}
const empty = (title, text = "", action = "", ic = "archive") => `<div class="empty">${icon(ic)}${title ? `<b>${esc(title)}</b>` : ""}${text ? `<p>${text}</p>` : ""}${action}</div>`;
const callout = (tone, title, text, actions = "") => `<div class="callout ${tone}"><div class="ct"><div class="ch">${esc(title)}</div><div class="cb">${text}</div></div>${actions ? `<div class="flex">${actions}</div>` : ""}</div>`;

const PAGES = {};
function route(p) {
  if (PAGES[p]) return PAGES[p];
  if (/^\/devices\/[^/]+$/.test(p)) return PAGES["/devices/:id"];
  if (/^\/devices\/[^/]+\/checks\/[^/]+$/.test(p)) return PAGES["/devices/:id/checks/:key"];
  return PAGES["/"];
}

async function render() {
  clearTimeout(pageTimer);
  pageToken++;
  onResize = null;
  if (!state.user) {
    try {
      const s = await GET("/api/session");
      state.setup = s.setup; state.version = s.version || ""; state.user = s.user || null;
      setInstance(s.instance);
    } catch (e) {
      app.innerHTML = `<div class="sheet-page"><div class="sheet"><div class="sheet-body">${esc(e.message)}</div></div></div>`;
      return;
    }
  }
  if (!state.user) { clearTimeout(ovTimer); state.ov = null; state.ovAt = 0; document.body.classList.remove("collapsed"); return state.setup ? setupPage() : loginPage(); }
  if (!qs(".topbar")) shell();
  else {
    qsa(".nav-link").forEach((a) => {
      const p = a.getAttribute("href");
      const on = location.pathname === p || (p !== "/" && location.pathname.startsWith(p));
      if (on) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
    });
  }
  const page = route(location.pathname);
  const el = qs("#page");
  const token = pageToken;
  try { await page(el, token); }
  catch (e) { if (token === pageToken) el.innerHTML = pageHead("Fehler") + callout("red", "Die Seite konnte nicht geladen werden", esc(e.message)); }
}

// every re-runs fn every ms while the page is shown (stops on navigation via pageToken).
function every(ms, token, fn) {
  if (token !== pageToken) return;
  const tick = async () => {
    if (token !== pageToken) return;
    try { await fn(); } catch (e) { /* keep last view */ }
    if (token === pageToken) pageTimer = setTimeout(tick, ms);
  };
  pageTimer = setTimeout(tick, ms);
}

// ---------- login & setup (x-anmeldeblatt) ----------
const NET_SVG = `<svg class="net-bg" viewBox="0 0 800 600" preserveAspectRatio="xMidYMid slice" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.2">
  <path d="M470 150h120v90h110M590 240v130h-80M510 370h-60v110M700 240v170h-90"/>
  <rect x="440" y="130" width="40" height="40" rx="2"/><rect x="690" y="220" width="40" height="40" rx="2"/><rect x="430" y="460" width="40" height="40" rx="2"/><rect x="590" y="390" width="40" height="40" rx="2"/>
  <path class="packet" stroke-width="3" d="M470 150h120v90h110"/><path class="packet" stroke-width="3" style="animation-delay:-2s" d="M590 240v130h-80M510 370h-60v110"/></svg>`;
function sheet(title, body, foot) {
  return `<div class="sheet-page"><div class="sheet-grid"></div>${NET_SVG}
    <button class="bar-btn sheet-theme" type="button" data-theme-icon aria-label="Erscheinungsbild wechseln">${themeIcon()}</button>
    <div class="sheet"><div class="sheet-strip"><span>${esc(title)}</span><span>v${esc(state.version)}</span></div>
    <div class="sheet-body"><h1 class="sheet-brand"><img src="/logo.svg" alt=""><span>PK95 Monitoring</span></h1>${state.instance ? `<p class="muted" style="margin:-20px 0 24px">${esc(state.instance)}</p>` : ""}${body}</div>
    ${foot ? `<div class="sheet-foot">${foot}</div>` : ""}</div></div>`;
}
function loginPage() {
  app.innerHTML = sheet("Anmeldung", `<form novalidate>
      <div class="form-error" hidden></div>
      ${field({ name: "username", label: "Benutzername", autocomplete: "username" })}
      ${field({ name: "password", label: "Passwort", type: "password", autocomplete: "current-password" })}
      <button class="btn block" type="submit">Anmelden</button></form>`, "Überwachung für Proxmox VE, Linux- und Windows-Server, SNMP-Geräte und Netzwerkdienste.");
  qs("[data-theme-icon]").onclick = setTheme;
  const form = qs("form");
  qs("input", form).focus();
  form.onsubmit = async (e) => {
    e.preventDefault();
    const v = formValues(form);
    try { const r = await POST("/api/login", v); state.user = r.user || v.username; app.innerHTML = ""; render(); }
    catch (err) { showErrors(form, err); }
  };
}
function setupPage() {
  app.innerHTML = sheet("Ersteinrichtung", `<form novalidate>
      <p class="muted" style="font-size:14px">Willkommen! Den <b>Einrichtungscode</b> findest du im Protokoll des Containers: <code>docker logs &lt;container&gt;</code> (bei Docker Compose: <code>docker compose logs</code>).</p>
      <div class="form-error" hidden></div>
      ${field({ name: "code", label: "Einrichtungscode", placeholder: "ABCD-EFGH", mono: true })}
      ${field({ name: "username", label: "Benutzername für dich", value: "admin", autocomplete: "username" })}
      ${field({ name: "password", label: "Passwort", type: "password", hint: "Mindestens 10 Zeichen.", autocomplete: "new-password" })}
      <button class="btn block" type="submit">Konto anlegen und starten</button></form>`, "Danach fügst du Proxmox VE, SNMP-Geräte und Netzwerk-Checks hinzu und installierst den Agent auf deinen Servern.");
  qs("[data-theme-icon]").onclick = setTheme;
  const form = qs("form");
  qs("input", form).focus();
  form.onsubmit = async (e) => {
    e.preventDefault();
    const v = formValues(form);
    try { const r = await POST("/api/setup", v); state.user = r.user || v.username; state.setup = false; app.innerHTML = ""; history.replaceState({}, "", "/"); render(); }
    catch (err) { showErrors(form, err); }
  };
}

// ---------- shared building blocks ----------
const SEV = { info: ["Info", "blue"], warning: ["Warnung", "amber"], critical: ["Kritisch", "rose"] };
const STATUS = { ok: ["Erfolgreich", "green"], warning: ["Warnung", "amber"], error: ["Fehler", "rose"] };

// eventItem is the short form of an event for the dashboard: status dot, title, source and age.
function eventItem(ev) {
  const st = ev.details && ev.details.status ? ev.details.status : { critical: "error", warning: "warning" }[ev.severity] || "ok";
  return `<a class="row ev-item" href="/events" data-go title="${esc(ev.body || "")}"><span class="sdot ${esc(st)}"></span><div class="grow"><div class="title">${esc(ev.title)}</div><div class="t-sub">${esc(ev.source)} · ${esc(fmtAgo(ev.ts))}</div></div></a>`;
}

// eventRow renders an event; with details (facts, rows, log) they open on click.
function eventRow(ev, compact) {
  const d = ev.details;
  const [l, t] = d && d.status && STATUS[d.status] ? STATUS[d.status] : (SEV[ev.severity] || SEV.info);
  const rows = (d && d.rows) || [];
  const counts = { ok: 0, warning: 0, error: 0 };
  rows.forEach((r) => { counts[STATUS[r.status] ? r.status : "ok"]++; });
  const chips = rows.length > 1 ? `<span class="flex" style="margin-top:4px">${Object.entries(counts).filter(([, n]) => n).map(([k, n]) => pill(`${n} ${{ ok: "erfolgreich", warning: "Warnung", error: "fehlgeschlagen" }[k]}`, STATUS[k][1])).join("")}</span>` : "";
  const head = `<span class="when" title="${fmtTime(ev.ts, true)}">${fmtTime(ev.ts)}</span>${pill(l, t)}<div class="grow"><div class="title"><b>${esc(ev.source)}:</b> ${esc(ev.title)}</div>${ev.body ? `<div class="body">${esc(ev.body)}</div>` : ""}${chips}</div>`;
  if (!d || compact || !(rows.length || (d.facts || []).length || (d.log || []).length || d.link)) return `<div class="row">${head}</div>`;
  const link = d.link && d.link.startsWith("/") ? d.link : "";
  return `<details class="row ev-details"><summary class="flex" style="align-items:flex-start;flex-wrap:nowrap">${head}<span class="ev-open">${icon("chevron-down")}</span></summary>
    <div class="ev-body">
      ${(d.facts || []).length ? `<table class="kv">${d.facts.map((f) => `<tr><td>${esc(f.label)}</td><td>${esc(f.value)}</td></tr>`).join("")}</table>` : ""}
      ${rows.length ? `<div class="ev-sub">${esc(d.rows_title || "Einträge")}</div><div class="table-wrap"><table class="resp ev-table"><thead><tr><th>Status</th><th>Eintrag</th><th>Zeit</th><th>Hinweis</th></tr></thead><tbody>
        ${rows.map((r) => { const [rl, rt] = STATUS[r.status] || STATUS.ok; return `<tr><td data-l="Status">${pill(rl, rt)}</td><td data-l="Eintrag">${r.kind ? `<span class="tag mono">${esc(r.kind)}</span> ` : ""}<b>${esc(r.id)}</b> ${esc(r.name || "")}</td>
          <td data-l="Zeit" class="nowrap">${esc(r.duration || (r.time ? fmtTime(r.time) : ""))}</td><td data-l="Hinweis" class="${r.status === "ok" ? "muted" : "bad"} small">${esc(r.info || "")}</td></tr>`; }).join("")}
        </tbody></table></div>` : ""}
      ${(d.log || []).length ? `<div class="ev-sub">Auszug aus dem Protokoll</div><pre class="log">${d.log.map(esc).join("\n")}</pre>` : ""}
      ${link ? `<div><a class="btn gray sm" href="${esc(link)}" data-go>Ansehen</a></div>` : ""}
    </div></details>`;
}

// devCard: compact status card of a device (Übersicht).
function devCard(d) {
  const st = devState(d);
  const bars = [["CPU", d.cpu], ["RAM", d.mem], ["Disk", d.disk]].filter(([, v]) => isNum(v));
  return `<a class="dev-card st-${st}" href="/devices/${enc(d.id)}" data-go>
    <div class="dc-head"><span class="dc-ic" title="${esc(TYPE_LABELS[d.type] || d.type)}">${icon(TYPE_ICONS[d.type] || "box")}</span>
      <div class="grow"><div class="dc-name">${esc(d.name)}${attTag(d)}</div><div class="dc-host mono">${esc(d.host || TYPE_LABELS[d.type] || "")}</div></div>${statePill(st)}</div>
    ${d.message ? `<div class="dc-msg">${esc(d.message)}</div>` : ""}
    ${bars.length ? `<div class="dc-bars">${bars.map(([l, v]) => `<div class="dc-bar"><span class="dc-l">${l}</span>${pctBar(v)}</div>`).join("")}</div>` : ""}
    <div class="dc-foot"><span>${esc(TYPE_LABELS[d.type] || d.type)}${d.platform ? " · " + esc(d.platform) : ""}</span><span title="Letzter Kontakt ${esc(fmtTime(d.last_poll, true))}">${esc(fmtAgo(d.last_poll))}</span></div></a>`;
}

// alertRow: one active alert in a list (Übersicht).
function alertRow(a) {
  return `<div class="row al-row"><span class="al-sev">${sevPill(a.severity)}</span><div class="grow">
      <div class="title"><a href="/devices/${enc(a.device_id)}" data-go><b>${esc(a.device_name)}</b></a> · ${esc(a.check_name)}</div>
      ${a.message ? `<div class="body">${esc(a.message)}</div>` : ""}
      <div class="t-sub" title="Seit ${esc(fmtTime(a.started, true))}">${esc(fmtSince(a.started))}${a.acked_by ? ` · quittiert von ${esc(a.acked_by)}` : ""}</div></div>
    ${!a.ended && !a.acked_by ? `<button class="btn gray sm" type="button" data-ack="${esc(a.id)}">${icon("check")} Quittieren</button>` : ""}</div>`;
}
function bindAck(root, after) {
  qsa("[data-ack]", root).forEach((b) => b.onclick = () => run(b, async () => {
    await POST(`/api/alerts/${enc(b.dataset.ack)}/ack`);
    toast("Alarm quittiert.");
    state.ovAt = 0;
    await Promise.all([after ? after() : null, loadOverview().catch(() => {})]);
  }));
}

// ---------- overview ----------
const TILES = [
  ["crit", "x", "Kritisch", "red"], ["warn", "warn", "Warnung", "amber"], ["ok", "check", "OK", "green"],
  ["unknown", "info", "Unbekannt", ""], ["maintenance", "wrench", "Wartung", "blue"], ["disabled", "power", "Deaktiviert", ""],
];

PAGES["/"] = async (el, token) => {
  const draw = async (fresh) => {
    const r = fresh || Date.now() - state.ovAt > 5000 || !state.ov ? await loadOverview() : state.ov;
    if (token !== pageToken) return;
    const devs = r.devices || [], alerts = r.alerts || [], c = Object.fromEntries(TILES.map(([k]) => [k, int((r.counts || {})[k])]));
    const head = (sub) => pageHead("Übersicht", sub, `<button class="btn gray" type="button" data-refresh>${icon("refresh")} Aktualisieren</button><button class="btn" type="button" data-add>${icon("plus")} Gerät hinzufügen</button>`);
    if (!devs.length) {
      el.innerHTML = `<div class="dash">` + head("Noch nichts eingerichtet.") +
        empty("Noch keine Geräte", "Proxmox VE (über die API), SNMP-Geräte wie Switches, USVs und Drucker sowie Netzwerk-Checks fügst du hier hinzu. Linux- und Windows-Server meldest du mit dem Agent an.",
          `<div class="flex" style="justify-content:center;margin-top:8px"><button class="btn" type="button" data-add2>${icon("plus")} Gerät hinzufügen</button><a class="btn gray" href="/agents" data-go>${icon("download")} Agent installieren</a></div>`, "server") +
        `<div class="panel"><div class="panel-head"><div class="panel-title">Erste Schritte</div></div><ol class="steps">
          <li><span><b>Proxmox VE</b> – API-Token anlegen (nur Leserechte) und über „Gerät hinzufügen“ eintragen. Knoten, VMs, Container, Speicher, Platten, Updates und Sicherungen werden dann überwacht.</span></li>
          <li><span><b>Server mit dem Agent</b> – unter <a href="/agents" data-go>Agents</a> den Befehl für Linux oder Windows kopieren und auf dem Server ausführen.</span></li>
          <li><span><b>Netzwerk</b> – Switches, USV und Drucker per SNMP, Websites und Dienste per Ping, HTTP, TCP, DNS oder TLS.</span></li>
          <li><span><b>Benachrichtigungen</b> unter <a href="/settings" data-go>Einstellungen</a> einrichten – per E-Mail, Telegram oder Push.</span></li></ol></div></div>`;
      qsa("[data-add], [data-add2]", el).forEach((b) => b.onclick = () => addDevice());
      qs("[data-refresh]", el).onclick = (e) => run(e.currentTarget, () => draw(true));
      return;
    }
    const crit = c.crit || 0, warn = c.warn || 0;
    const tone = crit ? "error" : warn ? "warning" : "ok";
    const bad = devs.filter((d) => ["crit", "warn"].includes(devState(d))).sort((a, b) => RANK[devState(b)] - RANK[devState(a)] || byName(a, b));
    const headline = tone === "ok" ? "Alles in Ordnung" : [crit ? `${crit} ${crit === 1 ? "Gerät" : "Geräte"} kritisch` : "", warn ? `${warn} mit Warnung` : ""].filter(Boolean).join(" · ");
    const lastPoll = devs.reduce((a, d) => Math.max(a, d.last_poll || 0), 0);
    const acked = alerts.filter((a) => a.acked_by).length;
    const fact = (label, value, sub) => `<div class="hero-fact"><dt>${esc(label)}</dt><dd>${value}</dd>${sub ? `<div class="hero-fact-sub">${sub}</div>` : ""}</div>`;
    const hero = `<section class="hero ${tone}" aria-live="polite">
        <div class="hero-main"><span class="hero-ic">${icon(tone === "ok" ? "check" : tone === "error" ? "x" : "warn")}</span>
          <div class="hero-text"><h2>${esc(headline)}</h2>
            ${tone === "ok" ? `<p>Alle aktiven Geräte melden sich und liegen innerhalb der Schwellwerte.${c.unknown ? ` ${c.unknown} ${c.unknown === 1 ? "Gerät hat" : "Geräte haben"} noch keine Daten.` : ""}</p>` :
              `<ul class="hero-list">${bad.slice(0, 6).map((d) => `<li><a href="/devices/${enc(d.id)}" data-go><span class="sdot ${SDOT[devState(d)]}"></span><span><b>${esc(d.name)}</b>${d.message ? ": " + esc(d.message) : ""}</span>${icon("arrow-right")}</a></li>`).join("")}
              ${bad.length > 6 ? `<li><a href="/devices?state=${crit ? "crit" : "warn"}" data-go><span class="sdot"></span><span>und ${bad.length - 6} weitere</span>${icon("arrow-right")}</a></li>` : ""}</ul>`}</div></div>
        <dl class="hero-facts">
          ${fact("Geräte", String(devs.length), `${(r.groups || []).length} ${(r.groups || []).length === 1 ? "Gruppe" : "Gruppen"}`)}
          ${fact("Aktive Alarme", String(alerts.length), alerts.length ? `${acked} quittiert` : "keine")}
          ${fact("Letzte Prüfung", esc(fmtAgo(lastPoll)), esc(fmtTime(lastPoll, true)))}
        </dl></section>`;
    const tiles = `<div class="tiles six">${TILES.map(([k, ic, l, t]) => `<a class="tile ${c[k] ? t : ""}" href="/devices?state=${k}" data-go title="Geräte mit Zustand „${l}“ anzeigen"><span class="ti">${icon(ic)}</span><span class="tx"><span class="tv">${c[k] || 0}</span><span class="tl">${l}</span></span></a>`).join("")}</div>`;

    const groups = groupBy([...devs].sort(byName), (d) => d.group || "");
    groups.sort(([a], [b]) => (a === "") - (b === "") || a.localeCompare(b, "de"));
    const groupPanels = groups.map(([g, list]) => {
      list.sort((a, b) => RANK[devState(b)] - RANK[devState(a)] || byName(a, b));
      const n = { crit: 0, warn: 0 };
      list.forEach((d) => { const s = devState(d); if (n[s] !== undefined) n[s]++; });
      return `<div class="panel flush"><div class="panel-head"><div class="panel-title">${esc(g || "Ohne Gruppe")} <span class="muted small">${list.length}</span></div>
        <div class="actions">${n.crit ? pill(`${n.crit} kritisch`, "rose") : ""}${n.warn ? pill(`${n.warn} Warnung`, "amber") : ""}<a class="btn gray sm" href="/devices${g ? "?group=" + enc(g) : "?group=-"}" data-go>Liste</a></div></div>
        <div class="dev-cards">${list.map(devCard).join("")}</div></div>`;
    }).join("");

    el.innerHTML = `<div class="dash">` + head(`${state.instance ? esc(state.instance) + " · " : ""}Stand ${fmtTime(Math.floor(state.ovAt / 1000))} · aktualisiert sich alle 15 Sekunden`) + hero + tiles +
      `<div class="dash-cols">
        <div class="dash-col">${groupPanels}</div>
        <div class="dash-col">
          <div class="panel flush"><div class="panel-head"><div class="panel-title">Aktive Alarme</div><a class="btn gray sm" href="/alerts" data-go>Alle</a></div>
            <div class="rows">${alerts.map(alertRow).join("") || `<div class="row muted">Keine aktiven Alarme.</div>`}</div></div>
          <div class="panel flush"><div class="panel-head"><div class="panel-title">Ereignisse</div><a class="btn gray sm" href="/events" data-go>Alle</a></div>
            <div class="rows">${(r.events || []).map(eventItem).join("") || `<div class="row muted">Noch keine Ereignisse.</div>`}</div></div>
        </div></div></div>`;
    qs("[data-refresh]", el).onclick = (e) => run(e.currentTarget, () => draw(true));
    qs("[data-add]", el).onclick = () => addDevice();
    bindAck(el, () => draw(true));
  };
  el.innerHTML = pageHead("Übersicht", "Lade …");
  await draw(false);
  every(15000, token, () => draw(false));
};

// ---------- devices ----------
PAGES["/devices"] = async (el, token) => {
  const p = new URLSearchParams(location.search);
  const f = { q: p.get("q") || "", state: p.get("state") || "", type: p.get("type") || "", group: p.get("group") || "" };
  let data = null;
  const syncUrl = () => {
    const u = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) if (v) u.set(k, v);
    history.replaceState({}, "", "/devices" + (u.toString() ? "?" + u : ""));
  };
  const drawList = () => {
    if (token !== pageToken || !data) return;
    const q = f.q.trim().toLowerCase();
    const all = data.devices || [];
    const list = all.filter((d) => {
      if (q && !`${d.name} ${d.host} ${d.group} ${d.platform || ""} ${d.message || ""}`.toLowerCase().includes(q)) return false;
      if (f.state && devState(d) !== f.state) return false;
      if (f.type && d.type !== f.type) return false;
      if (f.group && (f.group === "-" ? d.group : d.group !== f.group)) return false;
      return true;
    }).sort((a, b) => RANK[devState(b)] - RANK[devState(a)] || byName(a, b));
    const box = qs("[data-list]", el);
    box.innerHTML = list.length ? `<div class="panel flush"><div class="table-wrap"><table class="resp dev-table"><thead><tr><th>Zustand</th><th>Gerät</th><th>Typ</th><th>Gruppe</th><th>Checks</th><th>CPU</th><th>RAM</th><th>Disk</th><th>Kontakt</th><th></th></tr></thead><tbody>
      ${list.map((d) => { const st = devState(d); return `<tr class="${st === "disabled" ? "off" : ""}">
        <td data-l="Zustand">${statePill(st)}</td>
        <td data-l="Gerät" class="dt-name"><a class="t-main" href="/devices/${enc(d.id)}" data-go>${esc(d.name)}</a>${attTag(d)}<div class="t-sub mono">${esc(d.host || "—")}</div>${d.message && st !== "ok" ? `<div class="t-sub">${esc(d.message)}</div>` : ""}</td>
        <td data-l="Typ" class="dt-type"><span class="flex nowrap" style="gap:6px;flex-wrap:nowrap">${icon(TYPE_ICONS[d.type] || "box", "sm muted")}${esc(TYPE_LABELS[d.type] || d.type)}</span>${d.platform ? `<div class="t-sub">${esc(d.platform)}</div>` : ""}</td>
        <td data-l="Gruppe" class="small">${esc(d.group || "—")}</td>
        <td data-l="Checks" class="small nowrap">${checksSummary(d.checks)}</td>
        <td data-l="CPU" class="bar-cell">${isNum(d.cpu) ? pctBar(d.cpu) : '<span class="muted">—</span>'}</td>
        <td data-l="RAM" class="bar-cell">${isNum(d.mem) ? pctBar(d.mem) : '<span class="muted">—</span>'}</td>
        <td data-l="Disk" class="bar-cell">${isNum(d.disk) ? pctBar(d.disk) : '<span class="muted">—</span>'}</td>
        <td data-l="Kontakt" class="small nowrap" title="${esc(fmtTime(d.last_poll, true))}">${esc(fmtAgo(d.last_poll))}</td>
        <td class="act"><div class="row-actions">
          <button class="ib" type="button" title="Jetzt prüfen" aria-label="${esc(d.name)} jetzt prüfen" data-poll="${esc(d.id)}" ${d.enabled === false ? "disabled" : ""}>${icon("refresh")}</button>
          <button class="ib" type="button" title="Bearbeiten" aria-label="${esc(d.name)} bearbeiten" data-edit="${esc(d.id)}">${icon("pencil")}</button>
          <button class="ib red" type="button" title="Löschen" aria-label="${esc(d.name)} löschen" data-del="${esc(d.id)}">${icon("trash")}</button></div></td></tr>`; }).join("")}
      </tbody></table></div></div><p class="muted small" style="margin:0 12px">${list.length} von ${all.length} Geräten</p>` :
      (all.length ? empty("Nichts gefunden", "Kein Gerät passt zu den Filtern.", `<button class="btn gray" type="button" data-reset>Filter zurücksetzen</button>`, "search") :
        empty("Noch keine Geräte", "Proxmox VE, SNMP-Geräte und Netzwerk-Checks fügst du hier hinzu, Linux- und Windows-Server mit dem Agent.", `<div class="flex" style="justify-content:center"><button class="btn" type="button" data-add2>${icon("plus")} Gerät hinzufügen</button><a class="btn gray" href="/agents" data-go>Agent installieren</a></div>`, "server"));
    const find = (id) => all.find((d) => d.id === id);
    qsa("[data-poll]", box).forEach((b) => b.onclick = () => run(b, async () => {
      const r = await POST(`/api/devices/${enc(b.dataset.poll)}/poll`);
      const st = (r.status || {}).state;
      toast(`${find(b.dataset.poll)?.name || "Gerät"} geprüft${st ? ": " + (STATES[st] || STATES.unknown)[0] : ""}.`);
      await load();
    }));
    qsa("[data-edit]", box).forEach((b) => b.onclick = () => run(b, () => editDevice(b.dataset.edit, load)));
    qsa("[data-del]", box).forEach((b) => b.onclick = () => deleteDevice(find(b.dataset.del), load));
    const reset = qs("[data-reset]", box);
    if (reset) reset.onclick = () => { f.q = f.state = f.type = f.group = ""; syncUrl(); drawToolbar(); drawList(); };
    const add2 = qs("[data-add2]", box);
    if (add2) add2.onclick = () => addDevice(load);
  };
  const drawToolbar = () => {
    const groups = data.groups || [];
    const opt = (v, l, cur) => `<option value="${esc(v)}" ${v === cur ? "selected" : ""}>${esc(l)}</option>`;
    qs("[data-toolbar]", el).innerHTML = `<div class="filters toolbar" role="search">
      <input type="search" data-f="q" value="${esc(f.q)}" placeholder="Suchen: Name, Adresse, Gruppe …" aria-label="Geräte durchsuchen">
      <select data-f="state" aria-label="Zustand">${opt("", "Alle Zustände", f.state)}${Object.entries(STATES).map(([k, [l]]) => opt(k, l, f.state)).join("")}</select>
      <select data-f="type" aria-label="Typ">${opt("", "Alle Typen", f.type)}${Object.entries(TYPE_LABELS).map(([k, l]) => opt(k, l, f.type)).join("")}</select>
      <select data-f="group" aria-label="Gruppe">${opt("", "Alle Gruppen", f.group)}${groups.map((g) => opt(g, g, f.group)).join("")}${opt("-", "Ohne Gruppe", f.group)}</select></div>`;
    qsa("[data-f]", el).forEach((x) => {
      x.oninput = x.onchange = () => { f[x.dataset.f] = x.value; syncUrl(); drawList(); };
    });
  };
  const load = async () => {
    const r = await GET("/api/devices");
    if (token !== pageToken) return;
    const first = !data, oldGroups = data ? JSON.stringify(data.groups || []) : "";
    data = r; state.groups = r.groups || state.groups;
    // redraw the filters only when new groups appeared and nobody is typing in them
    const tb = qs("[data-toolbar]", el);
    if (first || (oldGroups !== JSON.stringify(r.groups || []) && !tb.contains(document.activeElement))) drawToolbar();
    drawList();
  };
  el.innerHTML = pageHead("Geräte", "Alles, was überwacht wird: Proxmox VE, Server mit Agent, SNMP-Geräte und Netzwerk-Checks.",
    `<button class="btn" type="button" data-add>${icon("plus")} Gerät hinzufügen</button>`) + `<div data-toolbar></div><div data-list>${loadingPanel()}</div>`;
  qs("[data-add]", el).onclick = () => addDevice(load);
  await load();
  every(30000, token, load);
};

async function deleteDevice(d, done) {
  if (!d) return;
  if (await confirmDialog({ title: `${d.name} löschen?`, text: "Das Gerät, seine Messwerte und seine Alarme werden gelöscht. Das lässt sich nicht rückgängig machen." + (d.type === "agent" ? " Der Agent auf dem Server meldet sich danach nicht mehr an – deinstalliere ihn dort mit <code>pk95agent uninstall</code>." : "") +
    (d.attached > 0 ? ` ${d.attached === 1 ? "Der zugeordnete Agent bleibt" : `Die ${int(d.attached)} zugeordneten Agents bleiben`} als eigenes Gerät erhalten.` : ""), button: "Löschen" }))
    await run(null, async () => { await DEL(`/api/devices/${enc(d.id)}`); state.ovAt = 0; if (done) await done(); }, `${d.name} gelöscht.`);
}

// ---------- add / edit device ----------
function addDevice(done) {
  formDialog({
    title: "Gerät hinzufügen", ic: "plus", button: "Weiter", text: "Was möchtest du überwachen?",
    fields: [{ name: "type", type: "radio", value: "proxmox", options: [
      ["proxmox", "Proxmox VE (API)", "Knoten, VMs, Container, Speicher, Platten, Updates und Sicherungen über einen API-Token."],
      ["agent", "Linux / Windows (Agent)", "Debian, Ubuntu, Windows Server … – der Agent meldet CPU, RAM, Platten, Dienste und Updates."],
      ["snmp", "SNMP-Gerät", "Switch, USV, Drucker, NAS – Schnittstellen, Akku, Toner, Speicher."],
      ["nut", "USV (NUT-Server)", "USV über Network UPS Tools, z. B. UniFi-USV – Akku, Restlaufzeit, Last, Spannungen."],
      ["net", "Netzwerk / Website", "Ping, HTTP(S), TCP-Port, DNS und TLS-Zertifikat – ohne Software auf dem Ziel."]] }],
    submit: async (v) => {
      if (v.type === "agent") { go("/agents"); return; }
      setTimeout(() => deviceForm(null, v.type || "proxmox", done), 0);
    },
  });
}

async function editDevice(id, done) {
  const r = await GET(`/api/devices/${enc(id)}`);
  deviceForm(r.device, r.device.type, done);
}

const NC_TYPES = [["ping", "Ping"], ["http", "HTTP(S)"], ["tcp", "TCP-Port"], ["dns", "DNS"], ["tls", "TLS-Zertifikat"]];
const NC_TARGET = {
  ping: ["Ziel", "leer = Host des Geräts"], http: ["URL", "https://example.org/"], tcp: ["Ziel (Host:Port)", "10.0.0.5:3389"],
  dns: ["Name", "example.org"], tls: ["Ziel (Host:Port)", "mail.example.org:993"],
};

// netChecksEditor: rows of network checks with type-specific fields and a "Testen" button per row.
function netChecksEditor(box, checks, hostValue) {
  let list = (checks || []).map((c) => ({ ...c }));
  const fld = (i, k, label, inner, cls = "") => `<div class="field ${cls}" data-field="checks.${i}.${k}"><label class="fl" for="nc-${i}-${k}">${esc(label)}</label>${inner}<span class="err" hidden></span></div>`;
  const inp = (i, k, v, attrs = "") => `<input type="text" id="nc-${i}-${k}" data-k="${k}" value="${esc(v ?? "")}" ${attrs} autocomplete="off" spellcheck="false">`;
  const num = (i, k, v, ph) => `<input type="number" id="nc-${i}-${k}" data-k="${k}" value="${v ? esc(v) : ""}" min="0" placeholder="${esc(ph)}">`;
  const sel = (i, k, v, opts) => `<select id="nc-${i}-${k}" data-k="${k}">${opts.map(([o, l]) => `<option value="${esc(o)}" ${o === v ? "selected" : ""}>${esc(l)}</option>`).join("")}</select>`;
  const row = (c, i) => {
    const t = c.type || "ping";
    return `<div class="ed-row nc-row" data-i="${i}">
      <div class="ed-grid">
        ${fld(i, "type", "Art", sel(i, "type", t, NC_TYPES))}
        ${fld(i, "name", "Name", inp(i, "name", c.name, `placeholder="z. B. Weboberfläche"`))}
        ${fld(i, "target", NC_TARGET[t][0], inp(i, "target", c.target, `class="mono" placeholder="${esc(NC_TARGET[t][1])}"`), "span2")}
        ${t === "http" ? fld(i, "method", "Methode", sel(i, "method", c.method || "GET", [["GET", "GET"], ["HEAD", "HEAD"]])) +
          fld(i, "expect_status", "Erwarteter Status", num(i, "expect_status", c.expect_status, "2xx/3xx")) +
          fld(i, "keyword", "Seite enthält", inp(i, "keyword", c.keyword, `placeholder="optional"`)) +
          `<div class="field cbf" data-field="checks.${i}.ignore_tls"><label class="check"><input type="checkbox" data-k="ignore_tls" ${c.ignore_tls ? "checked" : ""}><span>Zertifikatsfehler ignorieren</span></label><span class="err" hidden></span></div>` : ""}
        ${t === "dns" ? fld(i, "server", "DNS-Server", inp(i, "server", c.server, `class="mono" placeholder="leer = System"`)) +
          fld(i, "record_type", "Eintrag", sel(i, "record_type", c.record_type || "A", ["A", "AAAA", "CNAME", "MX", "TXT", "NS"].map((x) => [x, x]))) +
          fld(i, "expect", "Erwartete Antwort", inp(i, "expect", c.expect, `class="mono" placeholder="optional"`)) : ""}
        ${fld(i, "timeout", "Timeout (s)", num(i, "timeout", c.timeout, "10"))}
      </div>
      <div class="ed-actions"><button class="btn gray sm" type="button" data-nc-test>${icon("play")} Testen</button>
        <button class="ib red" type="button" data-nc-del title="Check entfernen" aria-label="Check entfernen">${icon("trash")}</button></div>
      <div class="nc-result" data-nc-out></div></div>`;
  };
  const readRow = (el, old) => {
    const g = (k) => { const x = qs(`[data-k="${k}"]`, el); return x ? (x.type === "checkbox" ? x.checked : x.value.trim()) : undefined; };
    const c = { id: old.id || rid(), type: g("type") || "ping", name: g("name") || "", target: g("target") || "" };
    if (c.type === "http") { c.method = g("method") || "GET"; c.expect_status = Number(g("expect_status")) || 0; c.keyword = g("keyword") || ""; c.ignore_tls = !!g("ignore_tls"); }
    if (c.type === "dns") { c.server = g("server") || ""; c.record_type = g("record_type") || "A"; c.expect = g("expect") || ""; }
    const to = Number(g("timeout"));
    if (to > 0) c.timeout = to;
    return c;
  };
  const sync = () => { list = qsa(".nc-row", box).map((el, i) => readRow(el, list[i] || {})); };
  const draw = () => {
    box.innerHTML = list.length ? list.map(row).join("") : `<div class="muted small">Keine Netzwerk-Checks.</div>`;
    qsa(".nc-row", box).forEach((el) => {
      const i = +el.dataset.i;
      qs('[data-k="type"]', el).onchange = () => { sync(); draw(); const t = qs(`.nc-row[data-i="${i}"] [data-k="target"]`, box); if (t) t.focus(); };
      qs("[data-nc-del]", el).onclick = () => { sync(); list.splice(i, 1); draw(); };
      const tb = qs("[data-nc-test]", el);
      tb.onclick = () => run(tb, async () => {
        sync();
        const out = qs(`.nc-row[data-i="${i}"] [data-nc-out]`, box);
        out.innerHTML = `<span class="spinner"></span> prüft …`;
        try {
          const r = await POST("/api/test/netcheck", { host: hostValue(), check: list[i] });
          out.innerHTML = `${statePill(r.state || "unknown")}<span>${esc(r.msg || "")}</span>${(r.metrics || []).map((m) => `<span class="mchip"><span class="muted">${esc(metricLabel(m.n))}</span> ${esc(fmtVal(m.v, mUnit(m)))}</span>`).join("")}`;
        } catch (e) { out.innerHTML = `${pill("Fehler", "rose")}<span class="bad">${esc(e.fields ? Object.values(e.fields).join(" ") : e.message)}</span>`; }
      });
    });
  };
  draw();
  return {
    add(type = "http") { sync(); list.push({ id: rid(), type, name: "", target: "" }); draw(); const rows = qsa(".nc-row", box); const t = qs('[data-k="target"]', rows[rows.length - 1]); if (t) t.focus(); },
    read() { sync(); return list; },
  };
}

// oidEditor: custom SNMP values (OID, name, unit, factor).
function oidEditor(box, oids) {
  let list = (oids || []).map((o) => ({ ...o }));
  const row = (o, i) => `<div class="ed-row oid-row" data-i="${i}"><div class="ed-grid">
      <div class="field span2" data-field="snmp.oids.${i}.oid"><label class="fl" for="oid-${i}-oid">OID</label><input type="text" id="oid-${i}-oid" data-k="oid" class="mono" value="${esc(o.oid)}" placeholder="1.3.6.1.4.1.…" autocomplete="off" spellcheck="false"><span class="err" hidden></span></div>
      <div class="field" data-field="snmp.oids.${i}.name"><label class="fl" for="oid-${i}-name">Name</label><input type="text" id="oid-${i}-name" data-k="name" value="${esc(o.name)}" placeholder="z. B. Lüfter 1" autocomplete="off"><span class="err" hidden></span></div>
      <div class="field" data-field="snmp.oids.${i}.unit"><label class="fl" for="oid-${i}-unit">Einheit</label><input type="text" id="oid-${i}-unit" data-k="unit" value="${esc(o.unit)}" placeholder="z. B. °C, %, rpm" autocomplete="off"><span class="err" hidden></span></div>
      <div class="field" data-field="snmp.oids.${i}.scale"><label class="fl" for="oid-${i}-scale">Faktor</label><input type="number" step="any" id="oid-${i}-scale" data-k="scale" value="${o.scale ? esc(o.scale) : ""}" placeholder="1"><span class="err" hidden></span></div>
    </div><div class="ed-actions"><button class="ib red" type="button" data-oid-del title="Entfernen" aria-label="OID entfernen">${icon("trash")}</button></div></div>`;
  const sync = () => {
    list = qsa(".oid-row", box).map((el) => {
      const g = (k) => qs(`[data-k="${k}"]`, el).value.trim();
      return { oid: g("oid"), name: g("name"), unit: g("unit"), scale: Number(g("scale").replace(",", ".")) || 0 };
    });
  };
  const draw = () => {
    box.innerHTML = list.map(row).join("") || `<div class="muted small">Keine eigenen Werte.</div>`;
    qsa(".oid-row", box).forEach((el) => { qs("[data-oid-del]", el).onclick = () => { sync(); list.splice(+el.dataset.i, 1); draw(); }; });
  };
  draw();
  return { add() { sync(); list.push({ oid: "", name: "", unit: "", scale: 0 }); draw(); const r = qsa(".oid-row", box); qs('[data-k="oid"]', r[r.length - 1]).focus(); }, read() { sync(); return list.filter((o) => o.oid || o.name); } };
}

// interfacePicker: SNMP interfaces whose link must be up. Filled from the config, then from "Verbindung testen".
function interfacePicker(box, chosen) {
  let items = (chosen || []).map((n) => ({ name: n, descr: "", speed: 0, oper: "", on: true }));
  const isUp = (o) => o === "up" || o === 1 || o === "1" || o === true;
  const sync = () => { const on = new Set(qsa("[data-if]:checked", box).map((x) => x.dataset.if)); items.forEach((x) => { x.on = on.has(x.name); }); };
  const draw = () => {
    if (!items.length) { box.innerHTML = `<div class="muted small">Noch keine Schnittstellen bekannt – „Verbindung testen“ liest sie vom Gerät.</div>`; return; }
    box.innerHTML = `<div class="pick-list">${items.map((x) => `<label class="check"><input type="checkbox" data-if="${esc(x.name)}" ${x.on ? "checked" : ""}><span><b>${esc(x.name)}</b>${x.descr && x.descr !== x.name ? ` <span class="muted small">${esc(x.descr)}</span>` : ""} ${x.oper !== "" ? (isUp(x.oper) ? pill("up", "green") : pill("down")) : ""} ${x.speed ? `<span class="muted small">${esc(fmtSpeed(x.speed))}</span>` : ""}</span></label>`).join("")}</div>
      <div class="flex mt"><button class="btn gray sm" type="button" data-if-up>Alle verbundenen</button><button class="btn gray sm" type="button" data-if-none>Keine</button></div>`;
    qs("[data-if-up]", box).onclick = () => { items.forEach((x) => { x.on = isUp(x.oper); }); draw(); };
    qs("[data-if-none]", box).onclick = () => { items.forEach((x) => { x.on = false; }); draw(); };
  };
  draw();
  return {
    set(list) {
      sync();
      const on = new Set(items.filter((x) => x.on).map((x) => x.name));
      items = (list || []).map((x) => ({ name: x.name, descr: x.descr || "", speed: x.speed || 0, oper: x.oper ?? "", on: on.has(x.name) }));
      // keep chosen interfaces the device did not report (e.g. currently missing)
      for (const n of on) if (!items.some((x) => x.name === n)) items.push({ name: n, descr: "nicht gefunden", speed: 0, oper: "", on: true });
      draw();
    },
    read() { sync(); return items.filter((x) => x.on).map((x) => x.name); },
  };
}

// SNMP_PROFILES: values of model.SNMPConfig.Profile with their labels.
const SNMP_PROFILES = { auto: "Automatisch erkennen", generic: "Allgemein (nur Standardwerte)", switch: "Switch", unifi: "UniFi-Switch", ups: "USV", printer: "Drucker", nas: "NAS" };

const PVE_TOKEN_CMDS = `pveum user add pk95mon@pve --comment "PK95 Monitoring"
pveum acl modify / --users pk95mon@pve --roles PVEAuditor
pveum user token add pk95mon@pve monitoring --privsep 0`;

function deviceForm(dev, type, done) {
  const edit = !!dev;
  const d0 = dev ? structuredClone(dev) : { type, enabled: true, checks: [], thresholds: [], rules: [] };
  type = d0.type || type;
  const px = d0.proxmox || { port: 8006, watch_guests: "onboot", ignore_guests: [] };
  const sn = d0.snmp || { version: "2c", port: 161, profile: "auto", interfaces: [], oids: [] };
  const nu = d0.nut || { port: 3493, ups: "", username: "" };
  const groups = [...new Set([...(state.groups || []), ...(d0.group ? [d0.group] : [])])].sort((a, b) => a.localeCompare(b, "de"));
  const hostReq = type === "proxmox" || type === "snmp" || type === "nut";
  const namePh = { proxmox: "z. B. Cluster Büro", snmp: "z. B. Switch Keller", nut: "z. B. USV Serverraum", net: "z. B. Website", agent: "" }[type];
  const fields = [
    { name: "name", label: "Name", value: d0.name, required: true, placeholder: namePh },
    { name: "host", label: type === "proxmox" ? "Adresse eines Knotens" : type === "nut" ? "NUT-Server (Host / IP)" : "Host / IP", value: d0.host, required: hostReq, mono: true,
      placeholder: type === "net" ? "optional, z. B. 192.168.1.1" : "192.168.1.10",
      hint: type === "agent" ? "Meldet der Agent selbst; nur für Ping-Checks und die Anzeige." : type === "net" ? "Standardziel für Ping-Checks ohne eigenes Ziel." : type === "nut" ? "Die USV selbst (z. B. UniFi) oder der Server, an dem sie per USB hängt." : "" },
    { name: "group", label: "Gruppe", value: d0.group, list: "dl-groups", placeholder: "z. B. Server, Netzwerk" },
    { name: "interval", label: type === "agent" ? "Meldeintervall (s)" : "Intervall (s)", type: "number", min: 0, value: d0.interval || "", placeholder: "60", hint: "Leer = 60 Sekunden." },
    { name: "notes", label: "Notizen", type: "textarea", value: d0.notes, wide: true, rows: 2, placeholder: "z. B. Standort, Ansprechpartner, Besonderheiten" },
    { name: "enabled", label: "Aktiv", type: "checkbox", value: d0.enabled !== false, hint: "Ausgeschaltet wird nichts abgefragt." },
    { name: "no_alerts", label: "Keine Alarme", type: "checkbox", value: d0.no_alerts, hint: "Zustände werden angezeigt, aber es gibt keine Alarme und Benachrichtigungen." },
  ];
  let extra = `<datalist id="dl-groups">${groups.map((g) => `<option value="${esc(g)}"></option>`).join("")}</datalist>`;
  if (type === "proxmox") {
    extra += `<div class="section"><div class="section-title">Proxmox VE</div><div class="form-grid">
      ${field({ name: "proxmox.port", label: "Port", type: "number", value: px.port || 8006 })}
      ${field({ name: "proxmox.token_id", label: "Token-ID", required: true, value: px.token_id, mono: true, placeholder: "pk95mon@pve!monitoring" })}
      ${field({ name: "proxmox.secret", label: "Secret", type: "password", required: !px.has_secret, mono: true, placeholder: px.has_secret ? "unverändert lassen" : "xxxxxxxx-xxxx-…" })}
      ${field({ name: "proxmox.fingerprint", label: "Fingerabdruck des Zertifikats", value: px.fingerprint, wide: true, mono: true, button: "Abrufen", hint: "Steht in Proxmox VE unter Knoten → System → Zertifikate. Leer = das Zertifikat muss von einer bekannten Stelle signiert sein." })}
      ${field({ name: "proxmox.watch_guests", label: "Gäste überwachen", type: "select", value: px.watch_guests || "onboot", options: [["onboot", "Gäste mit Autostart"], ["all", "Alle Gäste"], ["none", "Keine (nur Werte)"]], hint: "Diese Gäste müssen laufen, sonst gibt es einen Alarm." })}
      ${field({ name: "proxmox.ignore_guests", label: "Gäste ignorieren", value: (px.ignore_guests || []).join(", "), mono: true, placeholder: "z. B. 9000, 9001", hint: "VMIDs, die nie Alarm auslösen (Vorlagen, Testsysteme)." })}
      <div class="field wide"><div class="flex"><button class="btn gray" type="button" data-test-pve>${icon("signal")} Verbindung testen</button></div><div class="test-out" data-test-out></div></div>
      <details class="howto wide"><summary>So legst du den API-Token an</summary>
        <p class="hint">In der Shell eines Knotens (Weboberfläche → Knoten → Shell). Der Benutzer bekommt nur Leserechte (Rolle PVEAuditor):</p>
        ${codeBlock(PVE_TOKEN_CMDS)}
        <p class="hint">Die Ausgabe zeigt Token-ID (<code>pk95mon@pve!monitoring</code>) und Secret – das Secret erscheint nur einmal.</p></details>
    </div></div>`;
  }
  if (type === "snmp") {
    const secv = (k) => (sn["has_" + k] ? "unverändert lassen" : "");
    extra += `<div class="section"><div class="section-title">SNMP</div><div class="form-grid">
      ${field({ name: "snmp.version", label: "Version", type: "select", value: sn.version || "2c", options: [["1", "v1"], ["2c", "v2c"], ["3", "v3"]] })}
      ${field({ name: "snmp.port", label: "Port", type: "number", value: sn.port || 161 })}
      ${field({ name: "snmp.community", label: "Community", type: "password", mono: true, placeholder: secv("community") || "public", hint: "Nur-Lese-Community des Geräts, meist „public“." })}
      ${field({ name: "snmp.user", label: "Benutzer", value: sn.user, mono: true })}
      ${field({ name: "snmp.sec_level", label: "Sicherheitsstufe", type: "select", value: sn.sec_level || "authPriv", options: [["noAuthNoPriv", "Ohne Anmeldung"], ["authNoPriv", "Anmeldung, nicht verschlüsselt"], ["authPriv", "Anmeldung und Verschlüsselung"]] })}
      ${field({ name: "snmp.auth_proto", label: "Auth-Protokoll", type: "select", value: sn.auth_proto || "SHA", options: ["MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512"] })}
      ${field({ name: "snmp.auth_pass", label: "Auth-Passwort", type: "password", placeholder: secv("auth_pass") })}
      ${field({ name: "snmp.priv_proto", label: "Privacy-Protokoll", type: "select", value: sn.priv_proto || "AES", options: ["DES", "AES", "AES192", "AES256"] })}
      ${field({ name: "snmp.priv_pass", label: "Privacy-Passwort", type: "password", placeholder: secv("priv_pass") })}
      ${field({ name: "snmp.profile", label: "Profil", type: "select", value: sn.profile || "auto", options: Object.entries(SNMP_PROFILES), hint: "Bestimmt die zusätzlichen Werte (Ports, PoE, Akku, Toner …)." })}
      <div class="field wide"><div class="flex"><button class="btn gray" type="button" data-test-snmp>${icon("signal")} Verbindung testen</button></div><div class="test-out" data-test-out></div></div>
      <div class="field wide" data-field="snmp.interfaces"><span class="fl">Alarm bei Link-Ausfall</span><div data-ifs></div><span class="hint">Alle Ports werden angezeigt und aufgezeichnet. Angehakte Ports melden einen Alarm, wenn der Link weg ist.</span><span class="err" hidden></span></div>
      <div class="field wide" data-field="snmp.oids"><span class="fl">Eigene Werte (OIDs)</span><div class="ed-rows" data-oids></div><div><button class="btn gray sm" type="button" data-add-oid>${icon("plus")} OID hinzufügen</button></div><span class="err" hidden></span></div>
    </div></div>`;
  }
  if (type === "nut") {
    extra += `<div class="section"><div class="section-title">NUT-Server</div><div class="form-grid">
      ${field({ name: "nut.port", label: "Port", type: "number", min: 1, max: 65535, value: nu.port || 3493 })}
      ${field({ name: "nut.ups", label: "UPS-Name", value: nu.ups, mono: true, placeholder: "leer = alle", hint: "Name der USV auf dem NUT-Server. Leer = jede USV, die der Server meldet." })}
      ${field({ name: "nut.username", label: "Benutzer", value: nu.username, mono: true, placeholder: "optional", hint: "Nur nötig, wenn der Server zum Lesen eine Anmeldung verlangt." })}
      ${field({ name: "nut.password", label: "Passwort", type: "password", placeholder: nu.has_password ? "unverändert lassen" : "optional" })}
      <div class="field wide"><div class="flex"><button class="btn gray" type="button" data-test-nut>${icon("signal")} Verbindung testen</button></div><div class="test-out" data-test-out></div><div data-nut-ups></div></div>
    </div></div>`;
  }
  extra += `<div class="section"><div class="section-title">Netzwerk-Checks</div>
    <div class="hint">${type === "net" ? "Ein Netzwerk-Gerät besteht aus seinen Checks. Ping ohne Ziel prüft den Host des Geräts." : "Zusätzliche Prüfungen von hier aus, z. B. ob die Weboberfläche antwortet oder das Zertifikat bald abläuft."}</div>
    <div class="ed-rows" data-checks></div>
    <div class="flex"><button class="btn gray sm" type="button" data-add-check>${icon("plus")} Check hinzufügen</button></div></div>`;

  let nc, oids, ifs;
  const title = edit ? `${dev.name} bearbeiten` : { proxmox: "Proxmox VE hinzufügen", snmp: "SNMP-Gerät hinzufügen", nut: "USV (NUT) hinzufügen", net: "Netzwerk-Checks hinzufügen", agent: "Agent" }[type];
  const text = !edit ? { proxmox: "Ein Knoten genügt, auch bei einem Cluster. Zugang über einen API-Token mit Leserechten.", snmp: "Switch, USV, Drucker oder NAS mit eingeschaltetem SNMP.", nut: "USV über einen NUT-Server (Network UPS Tools) – z. B. eine UniFi-USV oder ein Server mit angeschlossener USV.", net: "Erreichbarkeit, Antwortzeit und Zertifikate von Diensten und Websites." }[type] : esc(TYPE_LABELS[type] || type);
  const pxValues = (v) => ({
    port: Number(v["proxmox.port"]) || 8006, token_id: (v["proxmox.token_id"] || "").trim(), secret: v["proxmox.secret"] || "",
    fingerprint: (v["proxmox.fingerprint"] || "").trim(), watch_guests: v["proxmox.watch_guests"] || "onboot",
    ignore_guests: (v["proxmox.ignore_guests"] || "").split(/[\s,;]+/).filter(Boolean),
  });
  const snValues = (v) => {
    const s = { version: v["snmp.version"] || "2c", port: Number(v["snmp.port"]) || 161, profile: v["snmp.profile"] || "auto", interfaces: ifs ? ifs.read() : sn.interfaces || [], oids: oids ? oids.read() : [] };
    if (s.version === "3") Object.assign(s, { user: v["snmp.user"].trim(), sec_level: v["snmp.sec_level"], auth_proto: v["snmp.auth_proto"], auth_pass: v["snmp.auth_pass"], priv_proto: v["snmp.priv_proto"], priv_pass: v["snmp.priv_pass"] });
    else s.community = v["snmp.community"];
    return s;
  };
  const nuValues = (v) => ({
    port: Number(v["nut.port"]) || 3493, ups: (v["nut.ups"] || "").trim(), username: (v["nut.username"] || "").trim(), password: v["nut.password"] || "",
  });
  formDialog({
    title, text, ic: TYPE_ICONS[type] || "server", cls: "xwide", fields, extra,
    button: edit ? "Speichern" : "Hinzufügen",
    onMount: (d) => {
      const form = qs("form", d);
      const host = () => (qs('[name="host"]', d).value || "").trim();
      nc = netChecksEditor(qs("[data-checks]", d), d0.checks, host);
      qs("[data-add-check]", d).onclick = () => nc.add(type === "net" ? "ping" : "http");
      if (type === "net" && !edit && !(d0.checks || []).length) { nc.add("ping"); qs('[name="name"]', d).focus(); }
      bindCopy(d);
      if (type === "proxmox") {
        const fb = qs('[data-fbtn="proxmox.fingerprint"]', d);
        fb.onclick = () => run(fb, async () => {
          showErrors(d, null);
          try {
            const r = await POST("/api/probe", { host: host(), port: Number(qs('[name="proxmox.port"]', d).value) || 8006 });
            qs('[name="proxmox.fingerprint"]', d).value = r.fingerprint;
            toast("Fingerabdruck abgerufen – bitte mit dem in Proxmox VE vergleichen.");
          } catch (e) { showErrors(d, e.fields ? e : Object.assign(e, { fields: { "proxmox.fingerprint": e.message } })); }
        });
        const tb = qs("[data-test-pve]", d), out = qs("[data-test-out]", d);
        tb.onclick = () => run(tb, async () => {
          showErrors(d, null);
          out.innerHTML = `<span class="spinner"></span> verbinde …`;
          try {
            const r = await POST("/api/test/proxmox", { id: dev ? dev.id : "", host: host(), proxmox: pxValues(formValues(form)) });
            const nodes = (r.nodes || []).map((n) => (typeof n === "string" ? n : n.node || n.name || ""));
            const guests = Array.isArray(r.guests) ? r.guests.length : r.guests;
            out.innerHTML = r.ok === false ? `${pill("Fehler", "rose")} <span class="bad">${esc(r.message || "Verbindung fehlgeschlagen.")}</span>` :
              `${pill("Verbunden", "green", true)} <span>Proxmox VE ${esc(r.version || "")} · ${nodes.length} ${nodes.length === 1 ? "Knoten" : "Knoten"}${nodes.length ? " (" + nodes.map(esc).join(", ") + ")" : ""}${isNum(guests) ? ` · ${guests} Gäste` : ""}</span>${r.message ? `<div class="t-sub">${esc(r.message)}</div>` : ""}`;
          } catch (e) { out.innerHTML = ""; showErrors(d, e); }
        });
      }
      if (type === "snmp") {
        ifs = interfacePicker(qs("[data-ifs]", d), sn.interfaces);
        oids = oidEditor(qs("[data-oids]", d), sn.oids);
        qs("[data-add-oid]", d).onclick = () => oids.add();
        const toggle = () => {
          const v3 = qs('[name="snmp.version"]', d).value === "3";
          ["snmp.user", "snmp.sec_level", "snmp.auth_proto", "snmp.auth_pass", "snmp.priv_proto", "snmp.priv_pass"].forEach((n) => { qs(`[data-field="${n}"]`, d).hidden = !v3; });
          qs('[data-field="snmp.community"]', d).hidden = v3;
          const lvl = qs('[name="snmp.sec_level"]', d).value;
          if (v3) {
            qs('[data-field="snmp.auth_proto"]', d).hidden = qs('[data-field="snmp.auth_pass"]', d).hidden = lvl === "noAuthNoPriv";
            qs('[data-field="snmp.priv_proto"]', d).hidden = qs('[data-field="snmp.priv_pass"]', d).hidden = lvl !== "authPriv";
          }
        };
        qs('[name="snmp.version"]', d).onchange = toggle;
        qs('[name="snmp.sec_level"]', d).onchange = toggle;
        toggle();
        const tb = qs("[data-test-snmp]", d), out = qs("[data-test-out]", d);
        tb.onclick = () => run(tb, async () => {
          showErrors(d, null);
          out.innerHTML = `<span class="spinner"></span> frage ab …`;
          try {
            const r = await POST("/api/test/snmp", { id: dev ? dev.id : "", host: host(), snmp: snValues(formValues(form)) });
            if (r.ok === false) { out.innerHTML = `${pill("Fehler", "rose")} <span class="bad">${esc(r.message || "Keine Antwort.")}</span>`; return; }
            const prof = { generic: "Allgemein", switch: "Switch", unifi: "UniFi-Switch", ups: "USV", printer: "Drucker", nas: "NAS" }[r.profile] || r.profile || "";
            out.innerHTML = `${pill("Antwortet", "green", true)} <span><b>${esc(r.sys_name || "")}</b>${prof ? ` · erkannt als ${esc(prof)}` : ""}${(r.interfaces || []).length ? ` · ${r.interfaces.length} Schnittstellen` : ""}</span>${r.sys_descr ? `<div class="t-sub">${esc(r.sys_descr)}</div>` : ""}${r.message ? `<div class="t-sub">${esc(r.message)}</div>` : ""}`;
            if (!qs('[name="name"]', d).value && r.sys_name) qs('[name="name"]', d).value = r.sys_name;
            if ((r.interfaces || []).length) ifs.set(r.interfaces);
          } catch (e) { out.innerHTML = ""; showErrors(d, e); }
        });
      }
      if (type === "nut") {
        const tb = qs("[data-test-nut]", d), out = qs("[data-test-out]", d), box = qs("[data-nut-ups]", d);
        const upsInput = qs('[name="nut.ups"]', d);
        const mark = () => qsa("[data-pick-ups]", box).forEach((b) => b.setAttribute("aria-pressed", b.dataset.pickUps === upsInput.value.trim()));
        upsInput.addEventListener("input", mark);
        tb.onclick = () => run(tb, async () => {
          showErrors(d, null);
          out.innerHTML = `<span class="spinner"></span> verbinde …`;
          box.innerHTML = "";
          try {
            const r = await POST("/api/test/nut", { id: dev ? dev.id : "", host: host(), nut: nuValues(formValues(form)) });
            if (r.ok === false) { out.innerHTML = `${pill("Fehler", "rose")} <span class="bad">${esc(r.message || "Keine Antwort.")}</span>`; return; }
            const ups = r.ups || [];
            out.innerHTML = `${pill("Verbunden", "green", true)} <span>${r.server ? `<b>${esc(r.server)}</b> · ` : ""}${ups.length === 1 ? "1 USV gefunden" : `${ups.length} USVs gefunden`}</span>${r.message ? `<div class="t-sub">${esc(r.message)}</div>` : ""}`;
            if (!ups.length) return;
            box.innerHTML = `<div class="nut-ups-list" role="group" aria-label="Gefundene USVs – eine übernehmen">${ups.map((u) => `<button type="button" class="nut-ups" data-pick-ups="${esc(u.name)}" aria-pressed="false">
                <span class="nu-main"><b class="mono">${esc(u.name)}</b>${u.desc && u.desc !== u.name ? ` <span class="muted">${esc(u.desc)}</span>` : ""}</span>
                <span class="nu-sub">${esc(u.model || "Modell unbekannt")}${u.status ? ` · ${esc(nutStatusText(u.status))}` : ""}</span></button>`).join("")}
              ${ups.length > 1 ? `<button type="button" class="nut-ups" data-pick-ups="" aria-pressed="false"><span class="nu-main"><b>Alle</b></span><span class="nu-sub">Jede USV des Servers überwachen</span></button>` : ""}</div>
              <span class="hint">Auswählen übernimmt den Namen ins Feld „UPS-Name“.</span>`;
            mark();
            qsa("[data-pick-ups]", box).forEach((b) => b.onclick = () => {
              upsInput.value = b.dataset.pickUps;
              mark();
              const u = ups.find((x) => x.name === b.dataset.pickUps);
              const nm = qs('[name="name"]', d);
              if (u && !nm.value.trim()) nm.value = u.desc || u.model || u.name;
            });
            if (ups.length === 1 && !qs('[name="name"]', d).value.trim()) qs('[name="name"]', d).value = ups[0].desc || ups[0].model || ups[0].name;
          } catch (e) { out.innerHTML = ""; showErrors(d, e); }
        });
      }
    },
    submit: async (v) => {
      const out = structuredClone(d0);
      out.type = type;
      out.name = (v.name || "").trim();
      out.host = (v.host || "").trim();
      out.group = (v.group || "").trim();
      out.notes = v.notes || "";
      out.interval = Number(v.interval) || 0;
      out.enabled = !!v.enabled;
      out.no_alerts = !!v.no_alerts;
      out.checks = nc.read();
      out.thresholds = out.thresholds || [];
      if (type === "proxmox") out.proxmox = pxValues(v);
      if (type === "snmp") out.snmp = snValues(v);
      if (type === "nut") out.nut = nuValues(v);
      const r = edit ? await PUT(`/api/devices/${enc(dev.id)}`, out) : await POST("/api/devices", out);
      state.ovAt = 0;
      toast(edit ? "Gespeichert." : `${out.name} hinzugefügt – die erste Abfrage läuft.`);
      if (!edit && r && r.id) go(`/devices/${enc(r.id)}`);
      else if (done) await done(r);
    },
  });
}

// ---------- device detail ----------
// The overview shows every monitored check as a card (tables for large groups); a check opens its
// own page with the charts. "Alle Diagramme" is the free multi-series view.
const DTABS = [["overview", "Übersicht", "apps"], ["charts", "Alle Diagramme", "chart"], ["alerts", "Alarme", "bell"], ["settings", "Einstellungen", "cog"]];

PAGES["/devices/:id"] = async (el, token) => {
  const id = decodeURIComponent(location.pathname.slice("/devices/".length));
  let data;
  try { data = await GET(`/api/devices/${enc(id)}`); }
  catch (e) {
    if (e.status !== 404) throw e;
    el.innerHTML = pageHead("Gerät nicht gefunden", "", "", ["/devices", "Geräte"]) + empty("Dieses Gerät gibt es nicht (mehr)", "", `<a class="btn" href="/devices" data-go>Zu den Geräten</a>`, "server");
    return;
  }
  if (token !== pageToken) return;
  // #agent-<id> (back link of an attached agent's check page) opens the overview at that agent
  const hash = location.hash.slice(1);
  let tab = DTABS.some(([k]) => k === hash) ? hash : "overview";
  let scrollAgent = hash.startsWith("agent-") ? decodeURIComponent(hash.slice("agent-".length)) : "";
  let seq = 0;
  el.innerHTML = `<div data-head></div><div class="tabs" role="tablist" aria-label="Ansicht">${DTABS.map(([k, l, ic]) => `<button type="button" role="tab" id="tab-${k}" aria-controls="tabpanel" data-tab="${k}" aria-selected="${k === tab}" tabindex="${k === tab ? 0 : -1}">${icon(ic)}${l}</button>`).join("")}</div><div data-body id="tabpanel" role="tabpanel"></div>`;
  const head = qs("[data-head]", el), body = qs("[data-body]", el);
  const reload = async () => { const r = await GET(`/api/devices/${enc(id)}`); if (token !== pageToken) return; data = r; state.ovAt = 0; drawHead(); if (tab === "overview") drawTab(); };
  const ovAlive = () => token === pageToken && tab === "overview";
  // view: the status with the device's current rules applied (see projectStatus).
  const view = () => projectStatus(data.status || {}, data.device.rules);
  // attachedViews: the agents attached to this device, each with its own rules applied.
  const attachedViews = () => (data.attached || []).filter((a) => a && a.device).map((a) => ({ device: a.device, status: projectStatus(a.status || {}, a.device.rules) }));
  // saveDevice changes the stored device: fresh GET, mutate, PUT (empty secrets keep the stored ones).
  // Saves run one after another, so quick clicks do not overwrite each other.
  let saveQ = Promise.resolve(), busy = 0, wantFocus = null;
  const saveDevice = (mutate) => {
    busy++;
    const p = saveQ.then(async () => {
      const cur = (await GET(`/api/devices/${enc(id)}`)).device;
      mutate(cur);
      return PUT(`/api/devices/${enc(id)}`, cur);
    });
    saveQ = p.catch(() => {});
    return p.finally(() => { busy--; });
  };
  // saveRules changes the rules with fn(rules); done runs after a successful save, before the redraw.
  const saveRules = (btn, fn, done, okText = "Gespeichert.") => {
    wantFocus = btn && btn.dataset.fk ? btn.dataset.fk : null;
    return run(btn, async () => {
      let rules = null;
      const r = await saveDevice((cur) => { cur.rules = rules = fn(cur.rules || []); });
      data.device.rules = (r && Array.isArray(r.rules)) ? r.rules : rules;
      if (done) done();
      await reload();
    }, okText);
  };

  const drawHead = () => {
    const dv = data.device, st = view(), att = attachedViews(), par = data.parent || null;
    let s = devState({ ...dv, state: st.state });
    // a parent shows the worst state of itself and its attached agents (as in the device list)
    if (s !== "disabled" && s !== "maintenance") for (const a of att) { const x = devState({ ...a.device, state: a.status.state }); if (RANK[x] > RANK[s] && STATES[x]) s = x; }
    const ag = dv.agent || {};
    const sub = [
      `<span class="flex" style="gap:6px;display:inline-flex">${icon(TYPE_ICONS[dv.type] || "box", "sm")}${esc(TYPE_LABELS[dv.type] || dv.type)}</span>`,
      dv.host ? `<span class="mono">${esc(dv.host)}</span>` : "",
      dv.group ? esc(dv.group) : "",
      dv.type === "agent" && ag.platform ? esc([ag.platform, ag.platform_version].filter(Boolean).join(" ")) : "",
      `letzter Kontakt <span title="${esc(fmtTime(st.last_poll, true))}">${esc(fmtAgo(st.last_poll))}</span>`,
      att.length ? `${att.length} ${att.length === 1 ? "Agent" : "Agents"}` : "",
    ].filter(Boolean).join(" · ");
    const mt = dv.maintenance && dv.maintenance.until > nowSec();
    let notes = "";
    if (par) notes += callout("", `Gehört zu ${par.name}`, `Dieser Agent ergänzt <a href="/devices/${enc(par.id)}" data-go>${esc(par.name)}</a>${dv.node ? ` als Host <b>${esc(dv.node)}</b>` : ""} und wird dort mit angezeigt. Checks, Werte, Schwellwerte und Alarme bleiben die eigenen; Alarme heißen „${esc(par.name)} / ${esc(attLabel(dv))}“.`,
      `<a class="btn gray sm" href="/devices/${enc(par.id)}" data-go>${icon("back")} Zu ${esc(par.name)}</a><button class="btn gray sm" type="button" data-detach>${icon("eject")} Lösen</button>`);
    else if (dv.type === "agent") (data.suggest || []).slice(0, 3).forEach((g, i) => {
      notes += callout("", `Gehört dieser Server zu ${g.name}${g.node ? ` (Knoten ${g.node})` : ""}?`,
        `Hostname oder IP-Adressen passen zu <b>${esc(g.name)}</b> (${esc(TYPE_LABELS[g.type] || g.type)}). Zugeordnet erscheint der Agent dort als Teil des Geräts – mit seinen eigenen Checks, Werten und Alarmen.`,
        `<button class="btn sm" type="button" data-suggest="${i}">${icon("link")} Zuordnen</button>`);
    });
    if (mt) notes += callout("", `Wartung bis ${fmtTime(dv.maintenance.until, true)}`, (dv.maintenance.note ? esc(dv.maintenance.note) + " – " : "") + "Alarme und Benachrichtigungen sind bis dahin stumm, die Werte werden weiter aufgezeichnet.", `<button class="btn gray sm" type="button" data-maint-end>${icon("stop")} Wartung beenden</button>`);
    if (dv.enabled === false) notes += callout("yellow", "Deaktiviert", "Dieses Gerät wird nicht abgefragt. Unter „Bearbeiten“ schaltest du es wieder ein.");
    if (st.error && dv.enabled !== false) notes += callout("red", "Nicht erreichbar", esc(st.error) + (st.last_ok ? ` · zuletzt erreichbar ${esc(fmtAgo(st.last_ok))} (${esc(fmtTime(st.last_ok, true))})` : ""));
    if (dv.no_alerts) notes += callout("", "Keine Alarme", "Für dieses Gerät gibt es keine Alarme und Benachrichtigungen – die Zustände werden nur angezeigt.");
    if (dv.type === "agent" && !st.last_poll) notes += callout("", "Wartet auf den Agent", "Der Agent hat noch keinen Bericht geschickt. Läuft der Dienst <code>pk95agent</code> auf dem Server?");
    head.innerHTML = pageHead(`${esc(dv.name)} ${statePill(s)}`, sub,
      `<button class="btn gray" type="button" data-poll ${dv.enabled === false ? "disabled" : ""}>${icon("refresh")} Jetzt prüfen</button>
       ${dv.type !== "agent" ? `<button class="btn gray" type="button" data-attach>${icon("desktop")} Agent hinzufügen</button>` : ""}
       <button class="btn gray" type="button" data-maint>${icon("wrench")} Wartung</button>
       <button class="btn gray" type="button" data-edit>${icon("pencil")} Bearbeiten</button>
       <button class="ib red" type="button" data-del title="Löschen" aria-label="Gerät löschen">${icon("trash")}</button>`, par ? [`/devices/${enc(par.id)}`, par.name] : ["/devices", "Geräte"]) + notes;
    qs("[data-poll]", head).onclick = (e) => run(e.currentTarget, async () => {
      const r = await POST(`/api/devices/${enc(id)}/poll`);
      if (r.status) data.status = r.status;
      state.ovAt = 0;
      drawHead(); if (tab === "overview") drawTab();
      toast("Geprüft.");
    });
    qs("[data-maint]", head).onclick = () => maintenanceDialog(data.device, reload);
    const me = qs("[data-maint-end]", head);
    if (me) me.onclick = () => run(me, async () => { await POST(`/api/devices/${enc(id)}/maintenance`, { until: 0, note: "" }); await reload(); }, "Wartung beendet.");
    qs("[data-edit]", head).onclick = (e) => run(e.currentTarget, async () => deviceForm((await GET(`/api/devices/${enc(id)}`)).device, dv.type, reload));
    qs("[data-del]", head).onclick = () => deleteDevice({ ...data.device, attached: att.length }, async () => go(par ? `/devices/${enc(par.id)}` : "/devices"));
    const ab = qs("[data-attach]", head);
    if (ab) ab.onclick = () => attachDialog(data.device, view(), reload);
    const db = qs("[data-detach]", head);
    if (db) db.onclick = async () => {
      if (!(await confirmDialog({ title: `${dv.name} von ${par.name} lösen?`, text: "Der Agent ist danach wieder ein eigenes Gerät in der Liste. Seine Werte, Einstellungen und Alarme bleiben erhalten.", button: "Lösen", tone: "", ic: "eject" }))) return;
      run(db, async () => { await POST(`/api/devices/${enc(id)}/detach`); seriesCache.clear(); await reload(); }, `${dv.name} ist wieder ein eigenes Gerät.`);
    };
    qsa("[data-suggest]", head).forEach((b) => b.onclick = () => {
      const g = (data.suggest || [])[+b.dataset.suggest];
      if (g) run(b, async () => { await POST(`/api/devices/${enc(g.id)}/attach`, { agent_id: id, node: g.node || "" }); await reload(); }, `${dv.name} gehört jetzt zu ${g.name}.`);
    });
  };

  const drawTab = async () => {
    const my = ++seq;
    const alive = () => my === seq && token === pageToken;
    onResize = null;
    try {
      if (tab === "overview") drawOverview();
      else if (tab === "charts") await chartsTab(body, data.device, data.status || {}, alive, attachedViews());
      else if (tab === "alerts") { body.innerHTML = ""; await alertsBlock(body, `device=${enc(id)}&state=all`, { device: false, emptyTitle: "Noch keine Alarme", emptyText: "Hier erscheint, wann welcher Check auffällig war." }, alive); }
      else await deviceSettingsTab(body, data.device, alive, reload, view());
    } catch (e) { if (alive()) body.innerHTML = callout("red", "Konnte nicht geladen werden", esc(e.message)); }
  };

  // drawOverview renders the checks and keeps focus, inner scroll positions and open panels.
  const drawOverview = () => {
    closeModeMenu(false);
    const ae = document.activeElement;
    const fk = wantFocus || (ae && body.contains(ae) ? ae.getAttribute("data-fk") : null);
    wantFocus = null;
    const scroll = Object.fromEntries(qsa("[data-keep-scroll]", body).map((x) => [x.dataset.keepScroll, x.scrollTop]));
    const open = Object.fromEntries(qsa("details[data-keep]", body).map((x) => [x.dataset.keep, x.open]));
    const st = view();
    const att = attachedViews();
    const list = ckViewMode() === "list";
    body.innerHTML = list ? svcList(data.device, st, att) : checksView(data.device, st, att);
    bindCopy(body);
    qsa("[data-keep-scroll]", body).forEach((x) => { if (scroll[x.dataset.keepScroll]) x.scrollTop = scroll[x.dataset.keepScroll]; });
    // panels keep the state the user gave them (open by default or closed by default)
    qsa("details[data-keep]", body).forEach((x) => { if (x.dataset.keep in open) x.open = open[x.dataset.keep]; });
    if (list) { applySvFilter(body, svcFilter.get(id) || { q: "", bad: false }); syncSel(body, svcSel.get(id) || new Set()); }
    if (fk) {
      const n = qs(`[data-fk="${CSS.escape(fk)}"]`, body);
      if (n && !n.closest("[hidden]")) {
        n.focus({ preventScroll: true });
        if (n.type === "search") n.setSelectionRange(n.value.length, n.value.length);
      }
    }
    if (scrollAgent) {
      const sec = qs(`[data-att="${CSS.escape(scrollAgent)}"]`, body);
      scrollAgent = "";
      if (sec) requestAnimationFrame(() => sec.scrollIntoView({ block: "start" }));
    }
    bindOverview(st);
    // sparklines: one request per device (and attached agent) and minute, never one per card
    loadSparks([id, ...att.map((a) => a.device.id)]).then((got) => { if (got && ovAlive()) fillSparks(body); }).catch(() => {});
  };
  const bindOverview = (st) => {
    const dv = data.device;
    const byKey = new Map([...(st.checks || []), ...(st.hidden || [])].map((c) => [c.key, c]));
    // per check
    qsa("[data-mode-key]", body).forEach((b) => b.onclick = () => {
      const key = b.dataset.modeKey, c = byKey.get(key) || { name: key };
      const rules = dv.rules || [], r = ruleFor(rules, key);
      const hint = !r ? "" : r.match === key ? "Eigene Einstellung für diesen Check." : `Zurzeit durch die Regel <code>${esc(r.match)}</code>.`;
      modeMenu(b, { title: c.name || key, current: ruleMode(rules, key), hint, onPick: (m) => saveRules(b, (rs) => setExactRule(rs, key, m)) });
    });
    // per group
    qsa("[data-mode-group]", body).forEach((b) => b.onclick = () => {
      const g = b.dataset.modeGroup, { keys, outside } = groupKeys(st, g);
      const plan = groupPlan(keys, outside), rules = dv.rules || [];
      const modes = new Set(keys.map((k) => ruleMode(rules, k)));
      const hint = [plan.patterns.length ? `${plan.patterns.length === 1 ? "Regel" : "Regeln"} ${plan.patterns.map((p) => `<code>${esc(p)}</code>`).join(", ")}` : "",
        plan.exact.length ? `${plan.exact.length === 1 ? "eine Einzelregel" : `${plan.exact.length} Einzelregeln`}` : ""].filter(Boolean).join(" und ") +
        ". Einzeln eingestellte Checks behalten ihre Einstellung." + (modes.size > 1 ? " Zurzeit gemischt." : "");
      modeMenu(b, { title: `Alle in dieser Gruppe: ${g}`, current: modes.size === 1 ? [...modes][0] : "", hint: hint.charAt(0).toUpperCase() + hint.slice(1),
        onPick: (m) => saveRules(b, (rs) => applyGroupMode(rs, groupPlan(keys, outside), m)) });
    });
    // hidden checks
    qsa("[data-unhide]", body).forEach((b) => b.onclick = () => saveRules(b, (rs) => setExactRule(rs, b.dataset.unhide, "alert")));
    qsa("[data-unhide-group]", body).forEach((b) => b.onclick = () => {
      const g = b.dataset.unhideGroup, { keys, outside } = groupKeys(st, g);
      const hk = new Set((st.hidden || []).map((h) => h.key));
      saveRules(b, (rs) => {
        let out = applyGroupMode(rs, groupPlan(keys, outside), "alert");
        for (const k of keys) if (hk.has(k) && ruleMode(out, k) === "off") out = setExactRule(out, k, "alert"); // exact "off" rules of single checks
        return out;
      });
    });
    // list or cards
    qsa("[data-ckview]", body).forEach((b) => b.onclick = () => {
      if (b.getAttribute("aria-pressed") === "true") return;
      lsSet(VIEW_KEY, b.dataset.ckview);
      wantFocus = b.dataset.fk;
      drawOverview();
    });
    // service list: filter, checkboxes and the actions for the checked checks
    const sel = svcSel.get(dv.id);
    if (sel && qs(".sv-sec", body)) {
      const f = svcFilter.get(dv.id) || { q: "", bad: false };
      svcFilter.set(dv.id, f);
      const refilter = () => { applySvFilter(body, f); syncSel(body, sel); };
      const q = qs("[data-svq]", body);
      if (q) q.oninput = () => { f.q = q.value; refilter(); };
      qsa("[data-svb]", body).forEach((b) => b.onclick = () => {
        f.bad = !!b.dataset.svb;
        qsa("[data-svb]", body).forEach((x) => x.setAttribute("aria-pressed", x === b));
        refilter();
      });
      qsa("input[data-sel]", body).forEach((cb) => cb.onchange = () => { if (cb.checked) sel.add(cb.dataset.sel); else sel.delete(cb.dataset.sel); syncSel(body, sel); });
      const setRows = (rows, on) => rows.filter((tr) => !tr.hidden).forEach((tr) => { const b = qs("input[data-sel]", tr); if (b) { if (on) sel.add(b.dataset.sel); else sel.delete(b.dataset.sel); } });
      qsa("input[data-selg]", body).forEach((cb) => cb.onchange = () => { setRows(groupRowsOf(cb.closest("tr")), cb.checked); syncSel(body, sel); });
      qsa("input[data-sela]", body).forEach((cb) => cb.onchange = () => { setRows(qsa("tr.sv-row", cb.closest("table")), cb.checked); syncSel(body, sel); });
      const clear = qs("[data-bulk-clear]", body);
      if (clear) clear.onclick = () => { sel.clear(); syncSel(body, sel); const n = qs("[data-svq]", body); if (n) n.focus(); };
      // a mode for the checked checks: pattern rules where they cover a whole family ("snmp:if:*"),
      // exact rules for the rest, so every checked check gets the mode
      qsa("[data-bulk]", body).forEach((b) => b.onclick = () => {
        const mode = b.dataset.bulk, keys = [...sel];
        if (!keys.length) return;
        const all = [...(st.checks || []), ...(st.hidden || [])].map((c) => c.key).filter((k) => k !== REACH_KEY);
        const outside = all.filter((k) => !sel.has(k));
        saveRules(b, (rs) => {
          let out = applyGroupMode(rs, groupPlan(keys, outside), mode);
          for (const k of keys) if (ruleMode(out, k) !== mode) out = setExactRule(out, k, mode);
          return out;
        }, () => { sel.clear(); wantFocus = "svq"; }, `${keys.length} ${keys.length === 1 ? "Dienst" : "Dienste"}: ${RULE_MODE_SHORT[mode]}.`);
      });
    }
    // switch ports: filter and "Alarm bei Link-Ausfall"
    qsa("[data-pf]", body).forEach((b) => b.onclick = () => {
      const f = b.dataset.pf, sec = b.closest(".ck-group");
      portFilters.set(dv.id, f);
      qsa("[data-pf]", sec).forEach((x) => x.setAttribute("aria-pressed", x === b));
      let n = 0;
      qsa("tr[data-pst]", sec).forEach((tr) => { tr.hidden = !portShown(tr.dataset.pst, f); if (!tr.hidden) n++; });
      const none = qs("tr.pt-none", sec);
      if (none) none.hidden = n > 0;
    });
    bindRowLinks(body);
    qsa("[data-port-alarm]", body).forEach((cb) => cb.onchange = async () => {
      const name = cb.dataset.portAlarm, descr = cb.dataset.descr, on = cb.checked;
      const same = (x) => lc(x) === lc(name) || (descr && lc(x) === lc(descr));
      const edit = (list) => { const out = (list || []).filter((x) => !same(x)); if (on) out.push(name); return out; };
      cb.disabled = true;
      try {
        let next = null;
        const r = await saveDevice((cur) => { cur.snmp = { ...(cur.snmp || {}) }; cur.snmp.interfaces = next = edit(cur.snmp.interfaces); });
        data.device.snmp = { ...(data.device.snmp || {}), interfaces: r && r.snmp && Array.isArray(r.snmp.interfaces) ? r.snmp.interfaces : next };
        const sec = cb.closest(".ck-group"), cnt = sec && qs("[data-port-count]", sec);
        if (cnt) cnt.textContent = portCount((view().checks || []).filter((c) => c.key.startsWith("snmp:if:") && (c.group || "Weitere") === (sec.dataset.group || "")), data.device.snmp.interfaces || []);
        toast(`Link-Alarm für ${name} ${on ? "eingeschaltet" : "ausgeschaltet"} – gilt ab der nächsten Abfrage.`);
      } catch (e) { if (cb.isConnected) cb.checked = !on; toastErr(e); }
      finally { if (cb.isConnected) { cb.disabled = false; cb.focus(); } }
    });
  };
  const tabs = qsa("[data-tab]", el);
  const select = (b, focus) => {
    tab = b.dataset.tab;
    tabs.forEach((x) => { x.setAttribute("aria-selected", x === b); x.tabIndex = x === b ? 0 : -1; });
    if (focus) b.focus();
    history.replaceState({}, "", location.pathname + (tab === "overview" ? "" : "#" + tab));
    drawTab();
  };
  tabs.forEach((b, i) => {
    b.onclick = () => select(b);
    b.onkeydown = (e) => {
      if (e.key === "ArrowRight" || e.key === "ArrowLeft") { e.preventDefault(); select(tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length], true); }
    };
  });
  drawHead();
  await drawTab();
  every(30000, token, async () => {
    const r = await GET(`/api/devices/${enc(id)}`);
    if (token !== pageToken) return;
    data = r; drawHead();
    // no redraw while a mode menu is open or a change is being saved
    if (tab === "overview" && !modeMenuOpen && !busy) drawTab();
  });
};

function localInput(ts) {
  const d = new Date(ts * 1000), p = (x) => String(x).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

function maintenanceDialog(dv, done) {
  const active = dv.maintenance && dv.maintenance.until > nowSec();
  const d = formDialog({
    title: `Wartung: ${dv.name}`, ic: "wrench", button: active ? "Wartung ändern" : "Wartung starten", cls: "",
    text: "Während der Wartung gibt es für dieses Gerät keine Alarme und Benachrichtigungen. Die Werte werden weiter aufgezeichnet." + (active ? ` Läuft zurzeit bis <b>${esc(fmtTime(dv.maintenance.until, true))}</b>.` : ""),
    fields: [
      { name: "dur", type: "radio", label: "Dauer", value: "3600", options: [["3600", "1 Stunde"], ["14400", "4 Stunden"], ["86400", "1 Tag"], ["until", "Bis …"]] },
      { name: "until", label: "Bis", type: "datetime-local", value: localInput(active ? dv.maintenance.until : nowSec() + 2 * 3600), wide: true },
      { name: "note", label: "Notiz", value: active ? dv.maintenance.note : "", placeholder: "z. B. Update auf Proxmox VE 9", wide: true },
    ],
    extraButtons: active ? `<button class="btn gray" type="button" data-end>${icon("stop")} Wartung beenden</button>` : "",
    onMount: (dl) => {
      const show = () => { qs('[data-field="until"]', dl).hidden = formValues(qs("form", dl)).dur !== "until"; };
      qsa('[name="dur"]', dl).forEach((x) => x.onchange = show);
      show();
      qs('[name="dur"]:checked', dl).focus();
    },
    submit: async (v) => {
      let until;
      if (v.dur === "until") {
        until = Math.floor(new Date(v.until).getTime() / 1000);
        if (!until || until <= nowSec()) throw Object.assign(new Error("Bitte einen Zeitpunkt in der Zukunft wählen."), { fields: { until: "Liegt in der Vergangenheit." } });
      } else until = nowSec() + Number(v.dur || 3600);
      await POST(`/api/devices/${enc(dv.id)}/maintenance`, { until, note: (v.note || "").trim() });
      toast(`Wartung bis ${fmtTime(until, true)}.`);
      await done();
    },
  });
  const end = qs("[data-end]", d);
  if (end) end.onclick = () => run(end, async () => { await POST(`/api/devices/${enc(dv.id)}/maintenance`, { until: 0, note: "" }); d.close(); await done(); }, "Wartung beendet.");
}

// attachDialog adds an agent to a device: install a new one for it (one-liners with --device/--node)
// or attach an agent that is already registered and belongs to no device (POST /api/devices/{id}/attach).
function attachDialog(dv, st, done) {
  const nodes = (st.checks || []).filter((c) => c.key.startsWith("pve:node:")).map((c) => c.key.slice("pve:node:".length)).sort((a, b) => a.localeCompare(b, "de", { numeric: true }));
  const withNode = dv.type === "proxmox" && nodes.length > 0;
  let free = null, cmdSeq = 0;
  const d = formDialog({
    title: `Agent hinzufügen: ${dv.name}`, ic: "desktop", button: "Zuordnen", cls: "wide",
    text: "Der Agent ergänzt die Daten dieses Geräts um Werte aus dem Betriebssystem – CPU, Arbeitsspeicher, Platten, Dienste und Updates. Er behält eigene Checks, Schwellwerte und Alarme und wird hier mit angezeigt.",
    fields: [
      { name: "mode", type: "radio", value: "new", options: [["new", "Neu installieren", "Befehl auf dem Server ausführen – der Agent meldet sich direkt für dieses Gerät an."], ["existing", "Vorhandenen Agent zuordnen", "Ein schon angemeldeter Agent, der noch zu keinem Gerät gehört."]] },
      ...(withNode ? [{ name: "node", label: "Knoten", type: "select", value: "", options: [["", "Automatisch"], ...nodes.map((n) => [n, n])], hint: "Auf welchem Proxmox-Knoten läuft der Agent? Automatisch: der Knoten mit dem Hostnamen des Servers." }] : []),
      { name: "agent_id", label: "Agent", type: "select", options: [["", "Lade …"]], wide: true },
    ],
    extra: `<div class="att-cmds" data-att-cmds aria-live="polite"></div>`,
    onMount: (dl) => {
      const form = qs("form", dl), sb = qs('button[type="submit"]', dl), cmds = qs("[data-att-cmds]", dl);
      const nodeSel = qs('[name="node"]', dl), agSel = qs('[name="agent_id"]', dl);
      const mode = () => formValues(form).mode || "new";
      const show = () => {
        const ex = mode() === "existing";
        qs('[data-field="agent_id"]', dl).hidden = !ex;
        cmds.hidden = ex;
        sb.textContent = ex ? "Zuordnen" : "Fertig";
        sb.disabled = ex && free !== null && !free.length;
        if (nodeSel) {
          const opt0 = qs('option[value=""]', nodeSel);
          if (opt0) opt0.textContent = ex ? "Kein bestimmter Knoten" : "Automatisch";
          const nh = qs('[data-field="node"] .hint', dl);
          if (nh) nh.textContent = ex ? "Auf welchem Proxmox-Knoten läuft der Agent? Wird beim Auswählen des Agents nach seinem Hostnamen vorgeschlagen." : "Auf welchem Proxmox-Knoten läuft der Agent? Automatisch: der Knoten mit dem Hostnamen des Servers.";
        }
      };
      const drawAgents = () => {
        if (!free) return;
        agSel.innerHTML = free.length ? [`<option value="">Bitte wählen …</option>`, ...free.map((a) => `<option value="${esc(a.id)}">${esc(a.name)}${a.host && a.host !== a.name ? ` (${esc(a.host)})` : ""}${a.platform ? ` · ${esc(a.platform)}` : ""}</option>`)].join("")
          : `<option value="">Kein freier Agent</option>`;
        const f = qs('[data-field="agent_id"]', dl);
        let h = qs(".hint", f);
        if (!h) { h = document.createElement("span"); h.className = "hint"; f.insertBefore(h, qs(".err", f)); }
        h.textContent = free.length ? "Nur Agents, die noch zu keinem Gerät gehören." : "Alle angemeldeten Agents gehören schon zu einem Gerät. Installiere einen neuen oder löse einen Agent auf seiner Seite.";
        show();
      };
      // the one-liners depend on the node: GET /api/agents?device=<id>&node=<node>
      const loadCmds = async () => {
        const my = ++cmdSeq, node = nodeSel ? nodeSel.value : "";
        cmds.innerHTML = `<div class="flex muted small"><span class="spinner"></span> Lade die Befehle …</div>`;
        try {
          const r = await GET(`/api/agents?device=${enc(dv.id)}${node ? `&node=${enc(node)}` : ""}`);
          if (my !== cmdSeq || !dl.open) return;
          if (free === null) { free = (r.agents || []).filter((a) => !a.parent_id && a.id !== dv.id).sort(byName); drawAgents(); }
          cmds.innerHTML = `<div class="section-title">Linux <span class="pill">bash</span></div>${r.linux_cmd ? codeBlock(r.linux_cmd) : callout("yellow", "Nicht verfügbar", "Es fehlt die Agent-Datei für Linux.")}
            <div class="section-title">Windows <span class="pill">PowerShell</span></div>${r.windows_cmd ? codeBlock(r.windows_cmd) : callout("yellow", "Nicht verfügbar", "Es fehlt die Agent-Datei für Windows.")}
            <p class="hint">Als root bzw. in einer PowerShell „Als Administrator“ auf dem Server ausführen${node ? ` (Knoten <b>${esc(node)}</b>)` : ""}. Nach etwa einer Minute erscheint der Agent auf dieser Seite.</p>`;
          bindCopy(cmds);
        } catch (e) {
          if (my !== cmdSeq) return;
          cmds.innerHTML = callout("red", "Befehle konnten nicht geladen werden", esc(e.message));
          if (free === null) { free = []; drawAgents(); }
        }
      };
      qsa('[name="mode"]', dl).forEach((x) => x.onchange = () => { show(); if (mode() === "existing") agSel.focus(); });
      if (nodeSel) nodeSel.onchange = () => loadCmds();
      // preselect the node whose name equals the agent's short hostname
      agSel.onchange = () => {
        const a = (free || []).find((x) => x.id === agSel.value);
        if (!a || !nodeSel || nodeSel.value) return;
        const n = nodes.find((x) => lc(x) === shortHost(a.host) || lc(x) === shortHost(a.name));
        if (n) nodeSel.value = n;
      };
      show();
      loadCmds();
      qs('[name="mode"]:checked', dl).focus();
    },
    submit: async (v) => {
      if (v.mode !== "existing") return; // "Fertig": the agent attaches itself when it registers
      if (!v.agent_id) throw Object.assign(new Error("Bitte einen Agent auswählen."), { fields: { agent_id: "Bitte einen Agent auswählen." } });
      const a = (free || []).find((x) => x.id === v.agent_id);
      await POST(`/api/devices/${enc(dv.id)}/attach`, { agent_id: v.agent_id, node: v.node || "" });
      seriesCache.clear();
      toast(`${a ? a.name : "Agent"} gehört jetzt zu ${dv.name}.`);
      await done();
    },
  });
  return d;
}

// ---------- check rules (alert / quiet / off), see model.Device.CheckMode ----------
const REACH_KEY = "reach";
const RULE_MODES = [
  ["alert", "Überwachen", "bell", "Zustand zählt, Alarm bei Problemen"],
  ["quiet", "Nur anzeigen – kein Alarm", "eye", "Werte werden aufgezeichnet, nie Warnung oder Alarm"],
  ["off", "Ausblenden", "eye-off", "Wird verworfen, keine Werte gespeichert"],
];
const RULE_MODE_SHORT = { alert: "Überwachen", quiet: "Nur anzeigen", off: "Ausblenden" };
const normMode = (m) => (m === "quiet" || m === "off" ? m : "alert");
const utf8Len = (s) => new TextEncoder().encode(String(s)).length; // Go's len() of a string
// globMatch mirrors model.GlobMatch: * matches any sequence (also ":" and "/").
function globMatch(pattern, s) {
  const parts = String(pattern).split("*");
  s = String(s);
  if (parts.length === 1) return pattern === s;
  if (!s.startsWith(parts[0])) return false;
  s = s.slice(parts[0].length);
  for (const p of parts.slice(1, -1)) {
    const i = s.indexOf(p);
    if (i < 0) return false;
    s = s.slice(i + p.length);
  }
  return s.endsWith(parts[parts.length - 1]);
}
// ruleFor returns the rule that decides a check key (exact > longest pattern, last on a tie) or null.
function ruleFor(rules, key) {
  if (key === REACH_KEY) return null;
  let win = null, best = -1;
  for (const r of rules || []) {
    const m = String(r.match || "");
    if (m === key) return r;
    if (m.includes("*") && utf8Len(m) >= best && globMatch(m, key)) { win = r; best = utf8Len(m); }
  }
  return win;
}
const ruleMode = (rules, key) => { const r = ruleFor(rules, key); return r ? normMode(r.mode) : "alert"; };
// setExactRule sets the mode of one check: the exact rule is dropped when the patterns give that mode anyway.
function setExactRule(rules, key, mode) {
  const rest = (rules || []).filter((r) => r.match !== key);
  return ruleMode(rest, key) === mode ? rest : [...rest, { match: key, mode }];
}
// groupPlan derives rules for all checks of a group: one pattern per key family ("snmp:if:*"),
// as short as possible without matching keys outside the group; keys without a family get exact rules.
function groupPlan(keys, outside) {
  const patterns = [], exact = [];
  const rec = (list, k) => {
    const parts = new Map();
    for (const key of list) {
      const seg = key.split(":");
      if (seg.length <= k) { exact.push(key); continue; }
      const pre = seg.slice(0, k).join(":") + ":";
      if (!parts.has(pre)) parts.set(pre, []);
      parts.get(pre).push(key);
    }
    for (const [pre, part] of parts) {
      const p = pre + "*";
      if (!outside.some((o) => globMatch(p, o))) patterns.push(p);
      else rec(part, k + 1);
    }
  };
  rec([...new Set(keys)].filter((k) => k !== REACH_KEY), 1);
  return { keys: [...new Set(keys)].filter((k) => k !== REACH_KEY), patterns, exact };
}
// applyGroupMode sets the mode of a group: pattern rules (exact rules of single checks keep winning).
function applyGroupMode(rules, plan, mode) {
  let out = [...(rules || [])];
  for (const p of plan.patterns) {
    out = out.filter((r) => r.match !== p);
    const covered = plan.keys.filter((k) => globMatch(p, k) && !out.some((r) => r.match === k));
    if (!covered.every((k) => ruleMode(out, k) === mode)) out.push({ match: p, mode });
  }
  for (const k of plan.exact) out = setExactRule(out, k, mode);
  return out;
}
// projectStatus applies the device's current rules to a status computed with older ones (the
// server applies new rules with the next poll or report), so a change shows at once.
function projectStatus(st, rules) {
  if (!st) return {};
  const out = { ...st, checks: [], hidden: (st.hidden || []).map((h) => ({ ...h })) };
  const hk = new Set(out.hidden.map((h) => h.key));
  let changed = false;
  for (const c of st.checks || []) {
    const m = ruleMode(rules, c.key);
    if (m === "off") {
      changed = true;
      if (!hk.has(c.key)) { out.hidden.push({ key: c.key, name: c.name, group: c.group }); hk.add(c.key); }
    } else if (m === "quiet" && c.mode !== "quiet") {
      changed = true;
      const bad = c.state === "warn" || c.state === "crit";
      out.checks.push({ ...c, mode: "quiet", muted: bad ? c.state : "", state: bad ? "ok" : c.state, pending: 0 });
    } else if (m === "alert" && c.mode === "quiet") {
      changed = true;
      out.checks.push({ ...c, mode: "", state: c.muted || c.state, muted: "" });
    } else out.checks.push(c);
  }
  for (const h of out.hidden) h.pending = ruleMode(rules, h.key) !== "off"; // shown again with the next poll
  if (changed) {
    const known = out.checks.filter((c) => c.state !== "unknown");
    out.state = known.length ? known.reduce((a, c) => (RANK[c.state] > RANK[a] ? c.state : a), "ok") : out.checks.length ? "unknown" : st.state;
  }
  return out;
}

// ---------- mode menu (popover with role=menu) ----------
let modeMenuOpen = null;
function closeModeMenu(focus = true) {
  const m = modeMenuOpen;
  if (!m) return;
  modeMenuOpen = null;
  m.el.remove();
  document.removeEventListener("pointerdown", m.outside, true);
  window.removeEventListener("scroll", m.place, true);
  window.removeEventListener("resize", m.close);
  m.btn.setAttribute("aria-expanded", "false");
  if (focus && m.btn.isConnected) m.btn.focus();
}
// modeMenu opens the Überwachen / Nur anzeigen / Ausblenden menu at btn; onPick(mode) runs after it closed.
function modeMenu(btn, { title, current, hint = "", onPick }) {
  const again = modeMenuOpen && modeMenuOpen.btn === btn;
  closeModeMenu(false);
  if (again) return;
  const el = document.createElement("div");
  const hid = "mm-hint-" + rid();
  el.className = "mode-menu";
  el.setAttribute("role", "menu");
  el.setAttribute("aria-label", title);
  if (hint) el.setAttribute("aria-describedby", hid);
  el.innerHTML = `<div class="mm-title" aria-hidden="true">${esc(title)}</div>
    ${RULE_MODES.map(([m, l, ic, sub]) => `<button type="button" role="menuitemradio" tabindex="-1" aria-checked="${m === current}" data-m="${m}">${icon(ic)}<span class="mm-l"><b>${esc(l)}</b><small>${esc(sub)}</small></span>${m === current ? icon("check", "mm-on") : ""}</button>`).join("")}
    ${hint ? `<div class="mm-hint" id="${hid}">${hint}</div>` : ""}`;
  document.body.appendChild(el);
  const items = qsa('[role="menuitemradio"]', el);
  const place = () => {
    if (!btn.isConnected) { closeModeMenu(false); return; }
    const r = btn.getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight, vw = innerWidth, vh = innerHeight;
    const left = Math.max(8, Math.min(r.right - w, vw - w - 8));
    const top = r.bottom + 4 + h > vh - 8 && r.top - 4 - h >= 8 ? r.top - 4 - h : Math.min(r.bottom + 4, Math.max(8, vh - h - 8));
    el.style.left = left + "px"; el.style.top = top + "px";
  };
  const outside = (e) => { if (!el.contains(e.target) && !btn.contains(e.target)) closeModeMenu(false); };
  modeMenuOpen = { el, btn, place, outside, close: () => closeModeMenu(false) };
  btn.setAttribute("aria-expanded", "true");
  place();
  document.addEventListener("pointerdown", outside, true);
  window.addEventListener("scroll", place, true);
  window.addEventListener("resize", modeMenuOpen.close);
  el.addEventListener("keydown", (e) => {
    const i = items.indexOf(document.activeElement);
    const to = (n) => { e.preventDefault(); items[(n + items.length) % items.length].focus(); };
    if (e.key === "ArrowDown") to(i + 1);
    else if (e.key === "ArrowUp") to(i < 0 ? items.length - 1 : i - 1);
    else if (e.key === "Home") to(0);
    else if (e.key === "End") to(items.length - 1);
    else if (e.key === "Escape" || e.key === "Tab") { e.preventDefault(); closeModeMenu(true); }
  });
  items.forEach((b) => b.onclick = () => { const m = b.dataset.m; closeModeMenu(true); if (m !== current) onPick(m); });
  (items.find((b) => b.dataset.m === current) || items[0]).focus();
}

// bindRowLinks: table rows (and toner lines) with data-href open their check page on a click
// anywhere outside the controls; the real link in the first cell serves keyboard and middle click.
function bindRowLinks(root) {
  qsa("[data-href]", root).forEach((row) => row.onclick = (e) => {
    if (e.target.closest("button, a, input, label, select, textarea, summary") || String(getSelection() || "")) return;
    if (e.metaKey || e.ctrlKey) { window.open(row.dataset.href, "_blank", "noopener"); return; }
    go(row.dataset.href);
  });
}

// ---------- check views ----------
const GROUP_ORDER = ["Status", "Cluster", "Knoten", "System", "Festplatten", "Speicher", "VMs", "Container", "VMs / Container", "Netzwerk", "Schnittstellen", "PoE", "USV", "Drucker", "Sensoren", "Platten", "Dienste", "Updates", "Sicherungen", "Eigene Werte", "Netzwerk-Checks"];
// tableKind: checks of large groups are table rows (Proxmox nodes, guests and storage, switch ports,
// toner); every other check is a card.
function tableKind(key) {
  if (key.startsWith("pve:node:")) return "node";
  if (/^pve:(vm|ct):/.test(key)) return "guest";
  if (/^pve:(storage|zfs):/.test(key)) return "storage";
  if (key.startsWith("snmp:if:")) return "port";
  if (key.startsWith("snmp:toner:")) return "toner";
  return "";
}
const firstMetric = (c, pred) => (c.metrics || []).find(pred);
// An instance that already names the metric stands alone ("Magenta Toner", not "Toner Magenta Toner").
const instLabel = (n, i) => !i ? metricLabel(n) : i.toLowerCase().includes(metricLabel(n).toLowerCase()) ? i : metricLabel(n) + " " + i;
const metricChip = (m) => `<span class="mchip"><span class="muted">${esc(instLabel(m.n, m.i))}</span> ${esc(fmtVal(m.v, mUnit(m)))}</span>`;
const sinceCell = (c) => (c.since ? `<span title="Zustand seit ${esc(fmtTime(c.since, true))}">${esc(fmtSince(c.since))}</span>` : "");
const pendingTag = (c) => (c.pending ? ` <span class="tag" title="Grenze überschritten – wird noch bestätigt">${esc(c.pending)}× auffällig</span>` : "");
const reasonText = (c) => c.reason || c.msg || "";
const isQuiet = (c) => c.mode === "quiet";
const MUTED_TITLE = { warn: "Wäre Warnung", crit: "Wäre kritisch" };
// ckPill: state pill of a check; quiet checks show "Nur Anzeige" instead.
const ckPill = (c, dot = true) => (isQuiet(c) ? `<span class="pill" title="Nur Anzeige – dieser Check löst keinen Alarm aus">Nur Anzeige</span>` : statePill(c.state, dot));
// reasonHTML: the reason of a check; for a quiet check that would be warn/crit dimmed with a dot.
function reasonHTML(c, fallback = "") {
  const t = reasonText(c) || fallback;
  if (isQuiet(c) && MUTED_TITLE[c.muted]) return `<span class="ck-muted" title="${MUTED_TITLE[c.muted]}"><span class="sdot ${SDOT[c.muted]}" aria-hidden="true"></span><span>${esc(t || MUTED_TITLE[c.muted])}</span><span class="sr-only"> (${MUTED_TITLE[c.muted]})</span></span>`;
  return esc(t);
}
// modeBtn opens the mode menu of a check (not for the reachability check).
function modeBtn(c) {
  if (c.key === REACH_KEY) return "";
  const q = isQuiet(c);
  return `<button class="ib sm-ib ck-mode ${q ? "quiet" : ""}" type="button" data-mode-key="${esc(c.key)}" data-fk="m:${esc(c.key)}" aria-haspopup="menu" aria-expanded="false" title="Überwachung: ${q ? "nur anzeigen" : "überwachen"}" aria-label="Überwachung von ${esc(c.name)} einstellen">${icon("eye")}</button>`;
}
// cardTone: colour of a check card, its left bar and its sparkline.
const cardTone = (c) => (isQuiet(c) ? "quiet" : ["ok", "warn", "crit"].includes(c.state) ? c.state : "unknown");

// checkPath is the URL of the check page; ":" stays readable, "/" is encoded.
const checkPath = (devId, key) => `/devices/${enc(devId)}/checks/${enc(key).replace(/%3A/gi, ":")}`;
// ckLink: the real link in the first cell of a check row (the whole row opens the page on click, too).
const ckLink = (devId, c, html) => `<a class="row-main" href="${checkPath(devId, c.key)}" data-go data-fk="r:${esc(c.key)}">${html}</a>`;
const rowLink = (devId, c) => `data-href="${checkPath(devId, c.key)}"`;
const instOf = (c, n) => { const m = (c.metrics || []).find((x) => x.n === n); return m ? m.i || "" : undefined; };

// tableOf renders a check table; the last column holds the mode button of each row.
// Heads starting with "#" are numeric columns, with "~" the sparkline column.
function tableOf(head, rows) {
  const th = (h) => h.startsWith("#") ? `<th class="num">${esc(h.slice(1))}</th>` : h.startsWith("~") ? `<th class="spark-h">${esc(h.slice(1))}</th>` : `<th>${esc(h)}</th>`;
  return `<div class="table-wrap"><table class="resp ck-table"><thead><tr>${head.map(th).join("")}<th><span class="sr-only">Überwachung</span></th></tr></thead><tbody>${rows.join("")}</tbody></table></div>`;
}
const actTd = (c) => `<td class="act ck-act">${modeBtn(c)}</td>`;

// ---------- sparklines (GET /api/devices/{id}/sparks: one request per device and minute) ----------
const SPARK_RANGE = "6h";
const sparkCache = new Map(); // device id → {at, missing, byKey: Map(check key → [series])}
const sparkLoading = new Map(); // device id → pending request
// loadSparks fetches the sparklines of the devices whose data are older than a minute; an older
// server without the endpoint (404) is asked again after 10 minutes, the cards render without.
// Returns true when something was requested.
async function loadSparks(ids) {
  const now = Date.now();
  const todo = [...new Set(ids)].filter((id) => {
    const c = sparkCache.get(id);
    return !sparkLoading.has(id) && (!c || now - c.at > (c.missing ? 600000 : 55000));
  });
  await Promise.all(todo.map((id) => {
    const p = (async () => {
      try {
        const r = await GET(`/api/devices/${enc(id)}/sparks?range=${SPARK_RANGE}`);
        const byKey = new Map();
        for (const s of r.series || []) {
          const k = s.check_key || "";
          if (!byKey.has(k)) byKey.set(k, []);
          byKey.get(k).push(s);
        }
        sparkCache.set(id, { at: Date.now(), byKey });
      } catch (e) {
        if (e.status === 404) sparkCache.set(id, { at: Date.now(), missing: true, byKey: new Map() });
      }
    })().finally(() => sparkLoading.delete(id));
    sparkLoading.set(id, p);
    return p;
  }));
  return todo.length > 0;
}
// sparkSVG draws bucket values (oldest first, null = gap) as area and line; b is drawn as a
// dashed second line on the same scale (sent next to received).
function sparkSVG(a, b, unit) {
  const all = [...a, ...(b || [])].filter(isNum).map(Number);
  if (!all.length) return "";
  let lo = Math.min(...all), hi = Math.max(...all);
  if (unit === "%") { lo = Math.min(0, lo); hi = Math.max(100, hi); }
  else if (["°C", "V", "d", "min", "rpm"].includes(unit)) { const pad = Math.max((hi - lo) * 0.25, Math.abs(hi) * 0.02, 0.5); lo -= pad; hi += pad; }
  else { lo = Math.min(0, lo); hi = hi > lo ? hi + (hi - lo) * 0.12 : lo + 1; }
  if (!(hi > lo)) hi = lo + 1;
  const W = 100, H = 30, n = Math.max(a.length, b ? b.length : 0);
  const X = (i) => (n > 1 ? (i * W) / (n - 1) : W / 2);
  const Y = (v) => H - 1 - ((v - lo) / (hi - lo)) * (H - 2);
  const f = (x) => x.toFixed(2);
  const segs = (vals) => {
    const out = [];
    let cur = [];
    vals.forEach((v, i) => { if (!isNum(v)) { if (cur.length) out.push(cur); cur = []; } else cur.push([X(i), Y(Number(v))]); });
    if (cur.length) out.push(cur);
    return out;
  };
  // a single point between gaps becomes a short dash so it stays visible
  const line = (g) => (g.length === 1 ? `M${f(Math.max(0, g[0][0] - 1))} ${f(g[0][1])}H${f(Math.min(W, g[0][0] + 1))}` : "M" + g.map(([x, y]) => `${f(x)} ${f(y)}`).join("L"));
  let s = `<svg class="spk-svg" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-hidden="true" focusable="false">`;
  for (const g of segs(a)) if (g.length > 1) s += `<path class="spk-a" d="${line(g)}L${f(g[g.length - 1][0])} ${H}L${f(g[0][0])} ${H}Z"/>`;
  if (b) for (const g of segs(b)) s += `<path class="spk-l2" d="${line(g)}"/>`;
  for (const g of segs(a)) s += `<path class="spk-l" d="${line(g)}"/>`;
  return s + "</svg>";
}
// sparkInner draws the sparkline of a check from the cache: names[0] (instance inst when given) and
// optionally names[1] as second line. Empty when there are no data (yet).
function sparkInner(devId, key, names, inst) {
  const c = sparkCache.get(devId);
  const list = (c && c.byKey.get(key)) || [];
  const pick = (n) => list.find((s) => s.name === n && (inst === undefined || (s.instance || "") === inst)) || list.find((s) => s.name === n);
  const a = pick(names[0]);
  if (!a) return "";
  const b = names[1] ? pick(names[1]) : null;
  return sparkSVG(a.values || [], b ? b.values || [] : null, a.unit || metricUnitOf(a.name));
}
// sparkBox reserves the space of a sparkline (no layout shift when the data arrive later) and
// fills it from the cache; fillSparks refills every box after a request.
function sparkBox(devId, key, sp, tone, cls) {
  return `<div class="${cls} spk t-${tone}" data-sdev="${esc(devId)}" data-skey="${esc(key)}" data-sn="${esc(sp.names.join(","))}"${sp.inst !== undefined ? ` data-si="${esc(sp.inst)}"` : ""}>${sparkInner(devId, key, sp.names, sp.inst)}</div>`;
}
function fillSparks(root) {
  qsa("[data-skey]", root).forEach((el) => { el.innerHTML = sparkInner(el.dataset.sdev, el.dataset.skey, el.dataset.sn.split(","), el.dataset.si); });
}

// ---------- check cards ----------
const SPARK_SKIP = new Set(["uptime", "procs", "printer.pages", "http.status", "mem.used", "mem.total", "disk.used", "disk.total", "storage.used", "storage.total", "poe.budget", "updates.security"]);
const cap1 = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : "");
// stateWord: the state of a check as a short word – its message when that is short ("Läuft", "Gestoppt").
function stateWord(c) {
  const m = String(c.msg || "").trim();
  if (m && m.length <= 28) return [cap1(m), true];
  return [(STATES[c.state] || STATES.unknown)[0], false];
}
// ofTotal: "6,1 von 16 GiB" (the unit once when both share it).
function ofTotal(used, total) {
  const a = fmtBytes(used), b = fmtBytes(total);
  return a.split(" ")[1] === b.split(" ")[1] ? `${a.split(" ")[0]} von ${b}` : `${a} von ${b}`;
}
// cardValue picks the primary value of a check card from its metrics: {text, sub, bar, low,
// rxtx, word, fromMsg, generic, spark: {names, inst}}. Counts shown as words (updates, failed
// units, event log errors) get no sparkline – a flat line of small integers is noise; their
// check page has the chart.
function cardValue(c) {
  const v = (n) => mval(c, n), has = (n) => isNum(v(n));
  const sp = (n, n2) => ({ names: n2 ? [n, n2] : [n], inst: instOf(c, n) });
  const fa = c.facts || {};
  if (has("ups.battery")) {
    const sub = [has("ups.runtime") ? `Restlaufzeit ${fmtVal(v("ups.runtime"), "min")}` : "", has("ups.load") ? `Last ${fmtPct(v("ups.load"))}` : ""].filter(Boolean).join(" · ");
    return { text: `Akku ${fmtPct(v("ups.battery"))}`, sub, spark: sp("ups.battery") };
  }
  if (has("poe.power")) {
    const bud = v("poe.budget"), u = has("poe.usage") ? v("poe.usage") : isNum(bud) && bud > 0 ? (v("poe.power") * 100) / bud : null;
    return { text: isNum(bud) ? `${fmtVal(v("poe.power"), "W")} von ${fmtVal(bud, "W")}` : fmtVal(v("poe.power"), "W"), sub: isNum(u) ? `Auslastung ${fmtPct(u)}` : "", bar: u, spark: sp("poe.power") };
  }
  for (const [rx, tx] of [["net.rx", "net.tx"], ["if.rx", "if.tx"]]) {
    if (!has(rx) && !has(tx)) continue;
    const er = v("if.errors");
    return { rxtx: [v(rx), v(tx)], text: `Empfangen ${fmtVal(v(rx), "B/s")}, gesendet ${fmtVal(v(tx), "B/s")}`,
      sub: isNum(er) && er > 0 ? `Fehler ${fmtVal(er, "1/s")}` : fa.Geschwindigkeit || "", spark: sp(rx, tx) };
  }
  if (has("http.time")) return { text: fmtVal(v("http.time"), "ms"), sub: [has("http.status") ? `HTTP ${fmtNum(v("http.status"), 0)}` : "", has("cert.days") ? `Zertifikat ${fmtVal(v("cert.days"), "d")}` : ""].filter(Boolean).join(" · "), spark: sp("http.time") };
  if (has("ping.rtt")) return { text: fmtVal(v("ping.rtt"), "ms"), sub: has("ping.loss") ? `${fmtPct(v("ping.loss"))} Verlust` : "", spark: sp("ping.rtt") };
  if (has("tcp.time")) return { text: fmtVal(v("tcp.time"), "ms"), sub: "Verbindungsaufbau", spark: sp("tcp.time") };
  if (has("dns.time")) return { text: fmtVal(v("dns.time"), "ms"), sub: "Antwortzeit", spark: sp("dns.time") };
  if (has("cert.days")) return { text: fmtVal(v("cert.days"), "d"), sub: "Zertifikat gültig", spark: sp("cert.days") };
  if (has("cpu.usage")) return { text: fmtPct(v("cpu.usage")), sub: has("cpu.load1") ? `Load ${fmtNum(v("cpu.load1"), 2)}` : "", spark: sp("cpu.usage") };
  if (has("mem.usage")) return { text: fmtPct(v("mem.usage")), sub: has("mem.used") && v("mem.total") > 0 ? ofTotal(v("mem.used"), v("mem.total")) : "", spark: sp("mem.usage") };
  if (has("swap.usage")) return { text: fmtPct(v("swap.usage")), sub: "belegt", spark: sp("swap.usage") };
  for (const n of ["disk", "storage"]) {
    if (!has(n + ".usage")) continue;
    const used = v(n + ".used"), tot = v(n + ".total");
    return { text: fmtPct(v(n + ".usage")), sub: isNum(used) && tot > 0 ? `${fmtBytes(Math.max(0, tot - used))} frei von ${fmtBytes(tot)}` : "", bar: v(n + ".usage"), spark: sp(n + ".usage") };
  }
  if (has("guest.cpu")) return { text: fmtPct(v("guest.cpu")), sub: has("guest.mem") ? `Arbeitsspeicher ${fmtPct(v("guest.mem"))}` : "", spark: sp("guest.cpu") };
  if (has("printer.toner")) return { text: fmtPct(v("printer.toner")), sub: "Füllstand", bar: v("printer.toner"), low: true, spark: sp("printer.toner") };
  if (has("temp")) { const m = firstMetric(c, (x) => x.n === "temp" && isNum(x.v)); return { text: fmtVal(m.v, "°C"), sub: m.i && !String(c.name).includes(m.i) ? m.i : "Temperatur", spark: sp("temp") }; }
  if (has("fan.rpm")) return { text: fmtVal(v("fan.rpm"), "rpm"), sub: "Drehzahl", spark: sp("fan.rpm") };
  if (has("updates.pending")) {
    const n = int(v("updates.pending")), s = int(v("updates.security"));
    return { word: true, text: n ? `${n.toLocaleString("de-DE")} ${n === 1 ? "Update" : "Updates"}${s ? ` (${s.toLocaleString("de-DE")} Sicherheit)` : ""}` : "Aktuell" };
  }
  if (has("services.failed")) { const n = int(v("services.failed")); return { word: true, text: n ? `${n.toLocaleString("de-DE")} fehlgeschlagen` : "Keine Fehler" }; }
  if (has("eventlog.errors")) { const n = int(v("eventlog.errors")); return { word: true, text: `${n.toLocaleString("de-DE")} Fehler`, sub: "in 24 Std." }; }
  if (has("uptime")) {
    const sub = ["Laufzeit", fa.Betriebssystem || fa.Modell || "", has("procs") ? `${fmtNum(v("procs"), 0)} Prozesse` : ""].filter(Boolean).join(" · ");
    return { text: fmtUptime(v("uptime")), sub };
  }
  const m = firstMetric(c, (x) => !SPARK_SKIP.has(x.n) && isNum(x.v));
  if (m) return { text: fmtVal(m.v, mUnit(m)), sub: m.i && !String(c.name).includes(m.i) ? instLabel(m.n, m.i) : metricLabel(m.n), spark: { names: [m.n], inst: m.i || "" }, generic: true };
  // SSDs of a Proxmox node (pve:disk:*) have no metrics, only the wear level as a fact
  if (fa["Verschleiß"]) return { word: true, text: `Verschleiß ${fa["Verschleiß"]}`, sub: [fa.Typ, fa["Größe"]].filter(Boolean).join(" · "), generic: true };
  const [w, fromMsg] = stateWord(c);
  return { word: true, text: w, fromMsg, generic: true, stateOnly: !fromMsg };
}
// cardMsg: the small line under the value – the reason of a problem; for cards that only show a
// state also the message of an ok check.
function cardMsg(c, v) {
  const muted = isQuiet(c) && MUTED_TITLE[c.muted];
  if (c.state !== "ok" || muted) {
    const t = reasonText(c);
    if (!t && !muted) return "";
    if (v.fromMsg && t === c.msg && !muted) return "";
    return reasonHTML(c);
  }
  return v.generic && !v.fromMsg && c.msg ? esc(c.msg) : "";
}
function thinBar(p, tone, low) {
  const w = Math.max(0, Math.min(100, p));
  const cls = tone === "crit" ? "bad" : tone === "warn" ? "warn" : tone === "quiet" || tone === "unknown" ? "mute" : low && p <= 25 ? "warn" : "";
  return `<span class="svc-track"><span class="fill ${cls}" style="width:${w.toFixed(1)}%"></span></span>`;
}
// svcCard: one monitored check as a card. The name is a link stretched over the card (Enter and
// click open the check page); the mode button sits above it and stays a separate control.
function svcCard(c, devId, { mode = true } = {}) {
  const v = cardValue(c), tone = cardTone(c);
  // a card whose value is only the state word ("Kritisch") shows no extra state pill
  const pillH = isQuiet(c) ? ckPill(c) : c.state !== "ok" && !v.stateOnly ? statePill(c.state) : "";
  const stTxt = isQuiet(c) ? "Nur Anzeige" : c.state !== "ok" && !v.stateOnly ? (STATES[c.state] || STATES.unknown)[0] : "";
  const aria = [c.name, v.text, v.sub, stTxt].filter(Boolean).join(", ");
  const val = v.rxtx ? `<span class="svc-rx" title="Empfangen"><span aria-hidden="true">↓</span> ${esc(fmtVal(v.rxtx[0], "B/s"))}</span><span class="svc-tx" title="Gesendet"><span aria-hidden="true">↑</span> ${esc(fmtVal(v.rxtx[1], "B/s"))}</span>` : esc(v.text);
  const msg = cardMsg(c, v);
  return `<div class="svc-card st-${tone}">
    <div class="svc-head"><a class="svc-link" href="${checkPath(devId, c.key)}" data-go data-fk="c:${esc(devId)}|${esc(c.key)}" aria-label="${esc(aria)} – Verlauf anzeigen">${esc(c.name)}</a>${pillH}${mode ? modeBtn(c) : ""}</div>
    <div class="svc-val${v.rxtx ? " rxtx" : v.word ? " word" : ""}" aria-hidden="true">${val}</div>
    ${v.sub ? `<div class="svc-sec" aria-hidden="true">${esc(v.sub)}</div>` : ""}
    ${isNum(v.bar) ? `<div class="svc-bar" aria-hidden="true">${thinBar(v.bar, tone, v.low)}</div>` : ""}
    ${v.spark ? sparkBox(devId, c.key, v.spark, tone, "svc-spark") : ""}
    ${msg || c.pending ? `<div class="svc-msg">${msg}${pendingTag(c)}</div>` : ""}</div>`;
}
// cardsGrid: the cards of a group; very many (Windows services) show problems first and fold the rest.
const CARD_LIMIT = 12;
function cardsGrid(list, devId, opts = {}, keep = "") {
  let cs = list, more = [];
  if (cs.length > CARD_LIMIT + 4) {
    cs = [...cs].sort(stateFirst);
    const n = Math.max(CARD_LIMIT, cs.filter((c) => c.state !== "ok").length);
    more = cs.slice(n); cs = cs.slice(0, n);
  }
  const grid = (l) => `<div class="svc-cards">${l.map((c) => svcCard(c, devId, opts)).join("")}</div>`;
  return grid(cs) + (more.length ? `<details class="svc-more" data-keep="${esc(keep)}"><summary>${more.length} weitere anzeigen</summary>${grid(more)}</details>` : "");
}

// ---------- switch ports (snmp:if:*) ----------
const portFilters = new Map(); // device id → all | up | down (kept across the 30-s refresh)
const PORT_ST = { up: ["Verbunden", "green"], down: ["Kein Link", ""], admin: ["Deaktiviert", ""], unknown: ["Unbekannt", ""] };
function portState(c) {
  const m = String(c.msg || ""), s = String((c.facts || {}).Status || "");
  if (/^Verbunden/.test(m)) return "up";
  if (/deaktiviert/i.test(m) || /deaktiviert/i.test(s)) return "admin";
  if (/kein link|down|nicht verbunden/i.test(m)) return "down";
  return "unknown";
}
const portName = (c) => c.key.slice("snmp:if:".length);
const lc = (s) => String(s ?? "").toLowerCase();
const portWatched = (watch, name, descr) => watch.some((w) => lc(w) === lc(name) || (descr && lc(w) === lc(descr)));
function portCount(list, watch) {
  const up = list.filter((c) => portState(c) === "up").length;
  const al = list.filter((c) => portWatched(watch, portName(c), (c.facts || {}).Beschreibung)).length;
  return `${list.length} ${list.length === 1 ? "Port" : "Ports"} · ${up} verbunden · ${al} mit Link-Alarm`;
}
const portShown = (ps, f) => f === "all" || (f === "up" ? ps === "up" : f === "down" ? ps === "down" || ps === "admin" : true);
// poeCell renders the PoE column of a port: delivered power (metric poe.port) or the PoE fact.
function poeCell(c) {
  const w = mval(c, "poe.port"), fa = c.facts || {}, f = String(fa.PoE || ""), cls = fa["PoE-Klasse"];
  const sub = cls ? `<div class="t-sub">Klasse ${esc(cls)}</div>` : "";
  if (/^fehler/i.test(f)) return pill("Fehler", "rose") + sub;
  if (isNum(w) && w > 0) return `<span class="poe-on">${esc(fmtVal(w, "W"))}</span>${sub}`;
  if (f) return `<span class="${/^aus/i.test(f) ? "muted" : ""}">${esc(f)}</span>${sub}`;
  return isNum(w) ? esc(fmtVal(w, "W")) + sub : "";
}
// portTitle: "Port 7" for "7" or "Gi1/0/7"; UniFi names its ports "Port 7" already.
const portTitle = (n) => (/^port\b/i.test(String(n)) ? String(n) : "Port " + n);
// portView: the port table; a click on a row (or the port name) opens the port's check page with its charts.
function portView(list, dv) {
  const watch = (dv.snmp && dv.snmp.interfaces) || [];
  const f = portFilters.get(dv.id) || "all";
  const hasPoe = list.some((c) => isNum(mval(c, "poe.port")) || (c.facts || {}).PoE);
  const hasNb = list.some((c) => (c.facts || {}).Nachbar || (c.facts || {})["Nachbar-Port"]);
  const cols = 9 + hasPoe + hasNb;
  const rows = [...list].sort(stateFirst).map((c) => {
    const name = portName(c), fa = c.facts || {}, ps = portState(c);
    const on = portWatched(watch, name, fa.Beschreibung);
    const rx = mval(c, "if.rx"), tx = mval(c, "if.tx"), er = mval(c, "if.errors");
    const [pl, ptone] = PORT_ST[ps];
    const tone = isQuiet(c) ? "" : c.state === "crit" ? "rose" : c.state === "warn" ? "amber" : ptone;
    const extra = ps === "unknown" ? reasonText(c) : c.reason && c.reason !== c.msg ? c.reason : "";
    const poe = hasPoe ? poeCell(c) : "", nb = fa.Nachbar || "", nbp = fa["Nachbar-Port"] || "";
    const hasTraffic = isNum(rx) || isNum(tx);
    return `<tr data-pst="${ps}" ${rowLink(dv.id, c)} ${portShown(ps, f) ? "" : "hidden"} class="row-link ${ps === "up" ? "" : "pt-off"}">
      ${td("Port", `<a class="row-main" href="${checkPath(dv.id, c.key)}" data-go data-fk="r:${esc(c.key)}" aria-label="${esc(portTitle(name))} – Verlauf anzeigen"><b>${esc(name)}</b></a>${pendingTag(c)}${fa.Beschreibung && fa.Beschreibung !== name ? `<div class="t-sub" title="${esc(fa.Beschreibung)}">${esc(fa.Beschreibung)}</div>` : ""}`, "pt-port")}
      ${td("Status", `<span class="flex pt-stl">${pill(pl, tone, !!tone && ps !== "up")}${isQuiet(c) ? `<span class="tag" title="Nur Anzeige – kein Alarm">Nur Anzeige</span>` : ""}</span>${extra || MUTED_TITLE[c.muted] && isQuiet(c) ? `<div class="t-sub">${reasonHTML({ ...c, reason: extra, msg: "" })}</div>` : ""}${isNum(er) && er > 0 ? `<div class="t-sub">Fehler ${esc(fmtVal(er, "1/s"))}</div>` : ""}`, "pt-st")}
      ${td("Geschwindigkeit", esc(fa.Geschwindigkeit || "—"), "small nowrap pt-meta" + (fa.Geschwindigkeit ? "" : " pt-empty"))}
      ${td("Alias", fa.Alias ? esc(fa.Alias) : '<span class="muted">—</span>', "small pt-meta pt-alias" + (fa.Alias ? "" : " pt-empty"))}
      ${hasPoe ? td("PoE", poe || '<span class="muted">—</span>', "small nowrap pt-meta pt-poe" + (poe ? "" : " pt-empty")) : ""}
      ${hasNb ? td("Nachbar", nb || nbp ? `${esc(nb || "—")}${nbp ? `<div class="t-sub mono">${esc(nbp)}</div>` : ""}` : '<span class="muted">—</span>', "small pt-meta pt-nb" + (nb || nbp ? "" : " pt-empty")) : ""}
      ${td("Empfangen", esc(isNum(rx) ? fmtVal(rx, "B/s") : "—"), "num small pt-meta" + (isNum(rx) ? "" : " pt-empty"))}
      ${td("Senden", esc(isNum(tx) ? fmtVal(tx, "B/s") : "—"), "num small pt-meta" + (isNum(tx) ? "" : " pt-empty"))}
      <td class="spark-cell pt-spark${hasTraffic ? "" : " pt-empty"}">${hasTraffic ? sparkBox(dv.id, c.key, { names: ["if.rx", "if.tx"], inst: instOf(c, "if.rx") ?? instOf(c, "if.tx") }, cardTone(c), "tspark") : ""}</td>
      <td class="pt-alarm"><label class="check"><input type="checkbox" data-port-alarm="${esc(name)}" data-descr="${esc(fa.Beschreibung || "")}" data-fk="pa:${esc(name)}" ${on ? "checked" : ""} aria-label="Alarm bei Link-Ausfall für Port ${esc(name)}"><span class="pt-alarm-t" aria-hidden="true">Alarm bei Link-Ausfall</span></label></td>
      <td class="act ck-act pt-mode">${modeBtn(c)}</td></tr>`;
  });
  const none = !list.some((c) => portShown(portState(c), f));
  return `<div class="port-bar"><span class="muted small" data-port-count>${esc(portCount(list, watch))}</span>
      <div class="seg" role="group" aria-label="Ports filtern">${[["all", "Alle"], ["up", "Verbunden"], ["down", "Ohne Link"]].map(([k, l]) => `<button type="button" data-pf="${k}" data-fk="pf:${k}" aria-pressed="${k === f}">${l}</button>`).join("")}</div></div>
    <div class="port-scroll" data-keep-scroll="ports"><table class="resp ck-table port-table"><thead><tr><th>Port</th><th>Status</th><th>Geschwindigkeit</th><th>Alias</th>${hasPoe ? "<th>PoE</th>" : ""}${hasNb ? "<th>Nachbar</th>" : ""}<th class="num">Empfangen</th><th class="num">Senden</th><th class="spark-h" title="Datenverkehr der letzten 6 Stunden">6 Std.</th><th class="pt-alarm-h">Alarm bei Link-Ausfall</th><th><span class="sr-only">Überwachung</span></th></tr></thead>
    <tbody>${rows.join("")}<tr class="pt-none" ${none ? "" : "hidden"}><td colspan="${cols}" class="muted small">Keine Ports in dieser Auswahl.</td></tr></tbody></table></div>`;
}

const seriesCache = new Map(); // device id → {at, series} of GET /api/devices/{id}/series
async function deviceSeries(id, maxAge = 60000) {
  const c = seriesCache.get(id);
  if (c && Date.now() - c.at < maxAge) return c.series;
  const r = await GET(`/api/devices/${enc(id)}/series`);
  seriesCache.set(id, { at: Date.now(), series: r.series || [] });
  return r.series || [];
}

const td = (label, html, cls = "") => `<td data-l="${esc(label)}"${cls ? ` class="${cls}"` : ""}>${html}</td>`;
const stateFirst = (a, b) => RANK[b.state] - RANK[a.state] || String(a.name).localeCompare(String(b.name), "de", { numeric: true });
const sparkTd = (devId, c, n) => `<td class="spark-cell">${isNum(mval(c, n)) ? sparkBox(devId, c.key, { names: [n], inst: instOf(c, n) }, cardTone(c), "tspark") : ""}</td>`;

const KIND_VIEWS = {
  node: (list, dv) => tableOf(["Knoten", "CPU", "Arbeitsspeicher", "Root-Platte", "Laufzeit", "~CPU · 6 Std."], list.map((c) => {
    const mu = mval(c, "mem.used"), mtot = mval(c, "mem.total");
    const agf = (c.facts || {}).Agent;
    return `<tr class="row-link" ${rowLink(dv.id, c)}>${td("Knoten", `<div class="flex" style="gap:8px">${ckPill(c)}${ckLink(dv.id, c, `<b>${esc(c.name)}</b>`)}</div>${reasonText(c) || isQuiet(c) && MUTED_TITLE[c.muted] ? `<div class="t-sub">${reasonHTML(c)}</div>` : ""}${agf ? `<div class="t-sub node-agent" title="Zugeordneter Agent">${icon("desktop", "sm")}<span>${esc(agf)}</span></div>` : ""}`)}
      ${td("CPU", pctBar(mval(c, "cpu.usage")), "bar-cell")}
      ${td("Arbeitsspeicher", pctBar(mval(c, "mem.usage"), mtot ? `${fmtBytes(mu)} von ${fmtBytes(mtot)}` : "") + (mtot ? `<div class="t-sub">${fmtBytes(mu)} von ${fmtBytes(mtot)}</div>` : ""), "bar-cell")}
      ${td("Root-Platte", pctBar(mval(c, "disk.usage")), "bar-cell")}
      ${td("Laufzeit", esc(isNum(mval(c, "uptime")) ? fmtUptime(mval(c, "uptime")) : "—"), "small nowrap")}${sparkTd(dv.id, c, "cpu.usage")}${actTd(c)}</tr>`;
  })),
  guest: (list, dv) => tableOf(["Zustand", "Gast", "Status", "CPU", "Arbeitsspeicher", "#Netz ein/aus", "~CPU · 6 Std."], [...list].sort((a, b) => RANK[b.state] - RANK[a.state] || (parseInt(a.key.split(":")[2], 10) || 0) - (parseInt(b.key.split(":")[2], 10) || 0)).map((c) => {
    const kind = c.key.startsWith("pve:ct:") ? "CT" : "VM";
    const mu = mval(c, "guest.mem_used");
    return `<tr class="row-link" ${rowLink(dv.id, c)}>${td("Zustand", ckPill(c))}
      ${td("Gast", `<span class="tag mono">${kind}</span> ${ckLink(dv.id, c, `<b>${esc(c.name.replace(/^(VM|CT)\s+/, ""))}</b>`)}${pendingTag(c)}`)}
      ${td("Status", `<span class="small">${reasonHTML(c, "—")}</span>`)}
      ${td("CPU", isNum(mval(c, "guest.cpu")) ? pctBar(mval(c, "guest.cpu")) : '<span class="muted">—</span>', "bar-cell")}
      ${td("Arbeitsspeicher", isNum(mval(c, "guest.mem")) ? pctBar(mval(c, "guest.mem")) + (mu ? `<div class="t-sub">${fmtBytes(mu)}</div>` : "") : '<span class="muted">—</span>', "bar-cell")}
      ${td("Netz ein/aus", isNum(mval(c, "guest.netin")) ? `${esc(fmtVal(mval(c, "guest.netin"), "B/s"))}<div class="t-sub">${esc(fmtVal(mval(c, "guest.netout"), "B/s"))}</div>` : '<span class="muted">—</span>', "num small")}${sparkTd(dv.id, c, "guest.cpu")}${actTd(c)}</tr>`;
  })),
  storage: (list, dv) => tableOf(["Zustand", "Speicher", "Belegung", "#Frei", "~Belegung · 6 Std."], [...list].sort(stateFirst).map((c) => {
    const u = firstMetric(c, (m) => mUnit(m) === "%"), used = firstMetric(c, (m) => /\.used$/.test(m.n)), tot = firstMetric(c, (m) => /\.total$/.test(m.n));
    return `<tr class="row-link" ${rowLink(dv.id, c)}>${td("Zustand", ckPill(c))}
      ${td("Speicher", `${ckLink(dv.id, c, `<b>${esc(c.name)}</b>`)}${pendingTag(c)}${reasonText(c) || isQuiet(c) && MUTED_TITLE[c.muted] ? `<div class="t-sub">${reasonHTML(c)}</div>` : ""}`)}
      ${td("Belegung", (u ? pctBar(u.v) : '<span class="muted">—</span>') + (used && tot ? `<div class="t-sub">${fmtBytes(used.v)} von ${fmtBytes(tot.v)}</div>` : ""), "bar-cell")}
      ${td("Frei", used && tot ? esc(fmtBytes(Math.max(0, tot.v - used.v))) : "—", "num small")}${u ? sparkTd(dv.id, c, u.n) : `<td class="spark-cell"></td>`}${actTd(c)}</tr>`;
  })),
  port: (list, dv) => portView(list, dv),
  toner: (list, dv) => `<div class="toner-list">${[...list].sort(stateFirst).map((c) => { const t = mval(c, "printer.toner"); return `<div class="toner row-link" ${rowLink(dv.id, c)}><a class="toner-name row-main" href="${checkPath(dv.id, c.key)}" data-go data-fk="r:${esc(c.key)}"><span class="toner-dot ${esc(tonerColor(c.name))}"></span>${esc(c.name.replace(/^Toner\s+/, ""))}</a>${levelBar(t, "", isQuiet(c) ? "" : c.state)}<span${isQuiet(c) && MUTED_TITLE[c.muted] ? ` class="ck-muted" title="${MUTED_TITLE[c.muted]}"` : ""}>${ckPill(c, false)}</span><span>${modeBtn(c)}</span></div>`; }).join("")}</div>`,
};
// nutStatusText turns NUT ups.status flags ("OL CHRG") into German words; unknown flags stay as they are.
// Text that is already German (the server's test answer, e.g. "Netzbetrieb, Akku lädt") is returned unchanged.
const NUT_FLAGS = { OL: "Netzbetrieb", OB: "Läuft auf Batterie", LB: "Akku schwach", HB: "Akku voll", RB: "Akku tauschen", CHRG: "Akku lädt", DISCHRG: "Akku entlädt",
  BYPASS: "Bypass", CAL: "Kalibrierung", OFF: "Ausgeschaltet", OVER: "Überlast", TRIM: "Spannung wird gesenkt", BOOST: "Spannung wird angehoben", FSD: "Wird heruntergefahren", ALARM: "Alarm" };
function nutStatusText(status) {
  if (/[a-zäöü,]/.test(String(status || ""))) return String(status);
  const f = String(status || "").trim().split(/\s+/).filter(Boolean);
  return f.length ? f.map((x) => NUT_FLAGS[x] || x).join(", ") : "";
}
function tonerColor(name) {
  const n = String(name).toLowerCase();
  if (/cyan/.test(n)) return "cyan";
  if (/magenta/.test(n)) return "magenta";
  if (/yellow|gelb/.test(n)) return "yellow";
  if (/black|schwarz|bk/.test(n)) return "black";
  return "";
}

// groupKeys lists the keys of a check group (shown and hidden checks) and the keys outside it.
function groupKeys(st, g) {
  const all = [...(st.checks || []), ...(st.hidden || [])].filter((c) => c.key !== REACH_KEY);
  const inG = (c) => (c.group || "Weitere") === g;
  return { keys: all.filter(inG).map((c) => c.key), outside: all.filter((c) => !inG(c)).map((c) => c.key) };
}
// groupModeBtn opens the mode menu for a whole check group.
function groupModeBtn(st, g) {
  const { keys } = groupKeys(st, g);
  if (!keys.length) return "";
  return `<button class="ib sm-ib ck-mode" type="button" data-mode-group="${esc(g)}" data-fk="g:${esc(g)}" aria-haspopup="menu" aria-expanded="false" title="Alle in dieser Gruppe" aria-label="Überwachung aller Checks der Gruppe ${esc(g)} einstellen">${icon("eye")}</button>`;
}

// hiddenPanel lists the checks switched off by rules (status.hidden) with "Wieder anzeigen".
function hiddenPanel(st) {
  const hid = st.hidden || [];
  if (!hid.length) return "";
  const off = hid.filter((h) => !h.pending);
  const groups = groupBy([...hid].sort(byName), (h) => h.group || "Weitere");
  groups.sort(([a], [b]) => a.localeCompare(b, "de"));
  return `<details class="panel flush sp ck-hidden" data-keep="hidden"><summary><span class="sp-title">${icon("eye-off")} Ausgeblendet (${off.length})</span>
      <span class="muted small">${off.length ? `${off.length === 1 ? "1 Check wird" : `${off.length} Checks werden`} nicht angezeigt und nicht aufgezeichnet` : `${hid.length === 1 ? "1 Check erscheint" : `${hid.length} Checks erscheinen`} mit der nächsten Abfrage wieder`}</span>${icon("chevron-down", "sp-chev")}</summary>
    <div class="rows">${groups.map(([g, list]) => {
      const n = list.filter((h) => !h.pending).length;
      return `<div class="row hid-head"><div class="grow"><b>${esc(g)}</b> <span class="muted small">${list.length}</span></div>${n > 1 ? `<button class="btn gray sm" type="button" data-unhide-group="${esc(g)}" data-fk="ug:${esc(g)}">${icon("eye")} Alle wieder anzeigen</button>` : ""}</div>` +
        list.map((h) => `<div class="row hid-row"><div class="grow"><div class="title"><b>${esc(h.name || h.key)}</b> <span class="muted small">· ${esc(h.group || "Weitere")}</span></div><div class="t-sub mono">${esc(h.key)}</div></div>
          ${h.pending ? `<span class="t-sub">Erscheint mit der nächsten Abfrage wieder</span>` : `<button class="btn gray sm" type="button" data-unhide="${esc(h.key)}" data-fk="u:${esc(h.key)}" aria-label="${esc(h.name || h.key)} wieder anzeigen">${icon("eye")} Wieder anzeigen</button>`}</div>`).join("");
    }).join("")}</div></details>`;
}

// ---------- attached agents (Device.ParentID) inside their parent ----------
const shortHost = (h) => lc(String(h || "").trim().split(".")[0]);
// attLabel names an attached agent inside its parent: the node, else the agent's name.
const attLabel = (dv) => dv.node || dv.name;
const groupIndex = (g) => { const i = GROUP_ORDER.indexOf(g); return i < 0 ? 99 : i; };
// attSection: one attached agent with its checks as cards (they open the agent's own check pages;
// rules are changed on the agent's page, so the cards have no mode button).
function attSection(a) {
  const dv = a.device, st = a.status || {}, ag = dv.agent || {};
  const s = devState({ ...dv, state: st.state });
  const checks = st.checks || [];
  const groups = groupBy(checks, (c) => c.group || "Weitere").sort(([x], [y]) => groupIndex(x) - groupIndex(y) || x.localeCompare(y, "de"));
  const sub = [
    dv.node && dv.name !== dv.node ? `<b>${esc(dv.name)}</b>` : "",
    ag.platform ? esc([ag.platform, ag.platform_version].filter(Boolean).join(" ")) : "",
    ag.version ? `Agent <span class="mono">${esc(ag.version)}</span>` : "",
    `letzter Kontakt <span title="${esc(fmtTime(st.last_poll, true))}">${esc(fmtAgo(st.last_poll))}</span>`,
  ].filter(Boolean).join(" · ");
  const body = !checks.length ? `<div class="att-empty muted small">${dv.enabled === false ? "Der Agent ist deaktiviert." : "Noch kein Bericht vom Agent – läuft der Dienst pk95agent auf dem Server?"}</div>` :
    `<div class="att-groups">${groups.map(([g, list]) => {
      const bad = list.filter((c) => c.state === "crit" || c.state === "warn").length;
      return `<div class="att-g"><div class="att-gh"><b>${esc(g)}</b><span class="muted small">${list.length}</span>${bad ? pill(`${bad} auffällig`, list.some((c) => c.state === "crit") ? "rose" : "amber") : ""}</div>
        ${cardsGrid(list, dv.id, { mode: false }, `att:${dv.id}:${g}`)}</div>`;
    }).join("")}</div>`;
  return `<section class="panel flush ck-group wide att-group st-${s}" data-att="${esc(dv.id)}" aria-label="${esc(`Agent auf ${attLabel(dv)}`)}">
    <div class="panel-head"><div class="panel-title att-title">${icon("desktop")}<span>Host ${esc(attLabel(dv))} · Agent</span>${statePill(s)}</div>
      <div class="actions"><a class="btn gray sm" href="/devices/${enc(dv.id)}" data-go aria-label="${esc(`Details des Agents ${dv.name}`)}">Details ${icon("arrow-right")}</a></div></div>
    <div class="att-meta t-sub">${sub}</div>
    ${st.message && s !== "ok" ? `<div class="att-msg">${esc(st.message)}</div>` : ""}
    ${body}</section>`;
}

// checksView renders the check groups of a device: cards, and tables for large groups;
// att = attached agents [{device, status}], shown after the node group when their node is known
// (Proxmox), else after the own checks.
function checksView(dv, st, att = []) {
  const checks = st.checks || [];
  const nodeGroup = (checks.find((c) => c.key.startsWith("pve:node:")) || {}).group;
  const nodes = new Set(checks.filter((c) => c.key.startsWith("pve:node:")).map((c) => c.key.slice("pve:node:".length)));
  const underNode = nodeGroup ? att.filter((a) => a.device.node && nodes.has(a.device.node)) : [];
  const rest = att.filter((a) => !underNode.includes(a));
  if (!checks.length) {
    const base = (st.hidden || []).length ? empty("Alle Checks ausgeblendet", "Unter „Ausgeblendet“ holst du sie zurück.", "", "eye-off") + hiddenPanel(st) :
      empty("Noch keine Werte", dv.enabled === false ? "Das Gerät ist deaktiviert." : dv.type === "agent" ? "Sobald der Agent seinen ersten Bericht schickt, erscheinen hier die Checks." : "Die erste Abfrage läuft gleich. „Jetzt prüfen“ startet sie sofort.", "", "clock");
    return att.length ? base + `<div class="ck-groups">${att.map(attSection).join("")}</div>` : base;
  }
  const groups = groupBy(checks, (c) => c.group || "Weitere");
  groups.sort(([a], [b]) => groupIndex(a) - groupIndex(b) || a.localeCompare(b, "de"));
  const summary = ckSummary(st, att, "cards");
  const notes = dvNotes(dv);
  const order = ["node", "storage", "guest", "port", "card", "toner"];
  return summary + notes + `<div class="ck-groups">` + groups.map(([g, list]) => {
    const bad = list.filter((c) => c.state === "crit" || c.state === "warn").length;
    const parts = groupBy(list, (c) => tableKind(c.key) || "card");
    parts.sort(([a], [b]) => order.indexOf(a) - order.indexOf(b));
    const inner = parts.map(([k, cs]) => k === "card" ? `<div class="ck-pad">${cardsGrid(cs, dv.id, {}, "more:" + g)}</div>`
      : k === "toner" ? `<div class="ck-pad">${KIND_VIEWS.toner(cs, dv)}</div>` : KIND_VIEWS[k](cs, dv)).join("");
    const cards = parts.reduce((n, [k, cs]) => n + (k === "card" ? cs.length : 0), 0);
    const wide = parts.some(([k]) => ["node", "guest", "port", "storage"].includes(k)) || cards > 4;
    const gb = groupModeBtn(st, g);
    return `<section class="panel flush ck-group ${wide ? "wide" : ""}" data-group="${esc(g)}"><div class="panel-head"><div class="panel-title">${esc(g)} <span class="muted small">${list.length}</span></div>${bad || gb ? `<div class="actions">${bad ? pill(`${bad} auffällig`, list.some((c) => c.state === "crit") ? "rose" : "amber") : ""}${gb}</div>` : ""}</div>${inner}</section>` +
      (g === nodeGroup ? underNode.map(attSection).join("") : "");
  }).join("") + rest.map(attSection).join("") + `</div>` + hiddenPanel(st);
}

// ckSummary: the counts line above the checks with the switch between list and cards.
function ckSummary(st, att, view) {
  const checks = st.checks || [];
  const counts = { ok: 0, warn: 0, crit: 0, unknown: 0 };
  checks.forEach((c) => { counts[c.state in counts ? c.state : "unknown"]++; });
  const quiet = checks.filter(isQuiet).length, hidden = (st.hidden || []).filter((h) => !h.pending).length;
  return `<div class="ck-summary">${Object.entries(counts).filter(([, n]) => n).map(([k, n]) => `<span class="flex" style="gap:6px"><span class="sdot ${SDOT[k]}"></span>${n} ${esc(k === "warn" && n !== 1 ? "Warnungen" : STATES[k][0])}</span>`).join("")}<span class="muted">· ${checks.length} ${checks.length === 1 ? "Check" : "Checks"}${quiet ? ` · ${quiet} nur Anzeige` : ""}${hidden ? ` · ${hidden} ausgeblendet` : ""}${att.length ? ` · ${att.length} ${att.length === 1 ? "Agent" : "Agents"}` : ""}</span>${view === "cards" ? `<span class="muted ck-hint">Für den Verlauf einen Check anklicken.</span>` : ""}${viewSeg(view)}</div>`;
}
const dvNotes = (dv) => (dv.notes ? `<div class="panel tight dv-notes"><span class="muted small">Notizen</span><div>${esc(dv.notes)}</div></div>` : "");

// ---------- service list (device overview, list view) ----------
// One row per check: state, name, a summary line with the current values, sparkline, since. Sections
// by monitoring mode (Überwacht / Nur Anzeige / Ausgeblendet); checked rows get a mode at once.
const VIEW_KEY = "pk95mon-ckview";
const ckViewMode = () => (lsGet(VIEW_KEY) === "cards" ? "cards" : "list");
const viewSeg = (cur) => `<div class="seg sv-view" role="group" aria-label="Darstellung">${[["list", "Liste", "list"], ["cards", "Kacheln", "apps"]].map(([k, l, ic]) => `<button type="button" data-ckview="${k}" data-fk="v:${k}" aria-pressed="${k === cur}">${icon(ic)}${l}</button>`).join("")}</div>`;
const svcSel = new Map(); // device id → Set of checked check keys (kept across the 30-s refresh)
const svcFilter = new Map(); // device id → {q, bad}
// svcBad: a check that needs a look – warn, crit, unknown, or quiet but would be warn/crit.
const svcBad = (c) => ["warn", "crit", "unknown"].includes(c.state) || (isQuiet(c) && !!MUTED_TITLE[c.muted]);
const rxtxText = (rx, tx) => `↓ ${fmtVal(rx, "B/s")}  ↑ ${fmtVal(tx, "B/s")}`;
// svcSummary: the current values of a check as one line ({html, text}); a problem's reason comes first.
function svcSummary(c) {
  const k = c.key, fa = c.facts || {}, v = (n) => mval(c, n), has = (n) => isNum(v(n));
  const parts = [], msgSegs = c.msg && c.state === "ok" ? String(c.msg).split(" · ").filter(Boolean) : [];
  if (k.startsWith("snmp:if:")) {
    parts.push(PORT_ST[portState(c)][0]);
    if (fa.Geschwindigkeit) parts.push(fa.Geschwindigkeit);
    if (has("if.rx") || has("if.tx")) parts.push(rxtxText(v("if.rx"), v("if.tx")));
    if (has("if.errors") && v("if.errors") > 0) parts.push(`Fehler ${fmtVal(v("if.errors"), "1/s")}`);
    if (fa.Alias) parts.push(fa.Alias);
    if (has("poe.port") && v("poe.port") > 0) parts.push(`PoE ${fmtVal(v("poe.port"), "W")}`);
    if (fa.Nachbar) parts.push(`Nachbar ${fa.Nachbar}${fa["Nachbar-Port"] ? ` (${fa["Nachbar-Port"]})` : ""}`);
  } else if (k.startsWith("pve:node:")) {
    // the node's message already holds CPU, RAM, load and uptime ("online · CPU 23 % · …")
    if (msgSegs.length) parts.push(...msgSegs);
    else {
      if (has("cpu.usage")) parts.push(`CPU ${fmtPct(v("cpu.usage"))}`);
      if (has("mem.usage")) parts.push(`RAM ${fmtPct(v("mem.usage"))}`);
      if (has("uptime")) parts.push(`Laufzeit ${fmtUptime(v("uptime"))}`);
    }
    if (v("mem.total") > 0) parts.push(`RAM ${ofTotal(v("mem.used"), v("mem.total"))}`);
    if (has("disk.usage")) parts.push(`Root ${fmtPct(v("disk.usage"))}`);
  } else if (/^pve:(vm|ct):/.test(k)) {
    if (msgSegs.length) parts.push(...msgSegs);
    else {
      if (has("guest.cpu")) parts.push(`CPU ${fmtPct(v("guest.cpu"))}`);
      if (has("guest.mem")) parts.push(`RAM ${fmtPct(v("guest.mem"))}`);
    }
    if (has("guest.netin") || has("guest.netout")) parts.push(`Netz ${rxtxText(v("guest.netin"), v("guest.netout"))}`);
  } else {
    const cv = cardValue(c);
    if (cv.rxtx) parts.push(rxtxText(cv.rxtx[0], cv.rxtx[1]));
    else if (cv.generic && !cv.word) {
      // generic checks list their values ("Spannung 230 V · Frequenz 50 Hz")
      for (const m of (c.metrics || []).filter((x) => isNum(x.v)).slice(0, 5)) parts.push(`${instLabel(m.n, m.i)} ${fmtVal(m.v, mUnit(m))}`);
    } else if (cv.sub && cv.sub.startsWith("Laufzeit")) parts.push(`Laufzeit ${cv.text}`, ...cv.sub.split(" · ").slice(1));
    else if (!cv.stateOnly) {
      // "1 % belegt", "Drehzahl 3.631 U/min", "63 % · 5,9 TiB frei von 16,0 TiB"
      const sub = cv.sub || "";
      if (/^[a-zäöü]/.test(sub)) parts.push(`${cv.text} ${sub}`);
      else if (sub && !/\d/.test(sub)) parts.push(`${sub} ${cv.text}`);
      else parts.push(cv.text, sub);
    }
    if ((cv.generic || cv.stateOnly) && !cv.fromMsg) parts.push(...msgSegs);
  }
  const why = c.state !== "ok" || (isQuiet(c) && MUTED_TITLE[c.muted]) ? reasonText(c) : "";
  // unique parts (spaces normalised: values use a narrow space before the unit, messages a normal one)
  const norm = (x) => String(x).replace(/[\s\u202f\u00a0]+/g, " ").trim().toLowerCase();
  const seen = new Set([why, why ? c.msg || "" : ""].flatMap((x) => String(x).split(" · ")).map(norm).filter(Boolean)), rest = [];
  for (const p of parts.filter(Boolean).map(String)) if (!seen.has(norm(p))) { seen.add(norm(p)); rest.push(p); }
  const tone = isQuiet(c) ? "quiet" : c.state;
  const html = [why ? `<span class="sv-why ${tone}">${reasonHTML(c)}</span>` : "", ...rest.map((p) => `<span>${esc(p)}</span>`)].filter(Boolean).join(`<span class="sv-dot" aria-hidden="true"> · </span>`);
  return { html: html || `<span class="muted">—</span>`, text: [why, ...rest].filter(Boolean).join(" · ") };
}
const svcSort = (list) => [...list].sort((a, b) => {
  const ga = a.group || "Weitere", gb = b.group || "Weitere";
  return groupIndex(ga) - groupIndex(gb) || ga.localeCompare(gb, "de") || byName(a, b);
});
const selBox = (attrs, label) => `<label class="sv-cb"><input type="checkbox" ${attrs} aria-label="${esc(label)}"></label>`;
// svcRow: one check; sel = the checked keys (null: no checkbox), mode: with the mode button.
function svcRow(c, devId, sel, mode) {
  const tone = cardTone(c), s = svcSummary(c), cv = cardValue(c), on = !!sel && sel.has(c.key);
  const box = sel && c.key !== REACH_KEY ? selBox(`data-sel="${esc(c.key)}" data-fk="s:${esc(devId)}|${esc(c.key)}" ${on ? "checked" : ""}`, `${c.name} auswählen`) : "";
  return `<tr class="row-link sv-row st-${tone}${on ? " sel" : ""}" ${rowLink(devId, c)} data-bad="${svcBad(c) ? 1 : ""}" data-q="${esc(lc([c.name, c.key, c.group, s.text].join(" ")))}">
    <td class="sv-sel">${box}</td><td class="sv-st">${ckPill(c)}</td>
    <td class="sv-name">${ckLink(devId, c, esc(c.name))}${pendingTag(c)}</td>
    <td class="sv-sum">${s.html}</td>
    <td class="spark-cell">${cv.spark ? sparkBox(devId, c.key, cv.spark, tone, "tspark") : ""}</td>
    <td class="sv-since">${sinceCell(c)}</td>
    <td class="act ck-act">${mode ? modeBtn(c) : ""}</td></tr>`;
}
// svcHiddenRow: a check switched off by a rule (no values, so no link).
function svcHiddenRow(h, sel) {
  const on = !h.pending && sel.has(h.key);
  return `<tr class="sv-row st-quiet sv-hid${on ? " sel" : ""}" data-bad="" data-q="${esc(lc([h.name, h.key, h.group].join(" ")))}">
    <td class="sv-sel">${h.pending ? "" : selBox(`data-sel="${esc(h.key)}" data-fk="s:|${esc(h.key)}" ${on ? "checked" : ""}`, `${h.name || h.key} auswählen`)}</td>
    <td class="sv-st">${pill("Ausgeblendet")}</td>
    <td class="sv-name">${esc(h.name || h.key)}</td>
    <td class="sv-sum"><span class="mono t-sub">${esc(h.key)}</span>${h.pending ? `<span class="sv-dot"> · </span><span>Erscheint mit der nächsten Abfrage wieder</span>` : ""}</td>
    <td class="spark-cell"></td><td class="sv-since"></td>
    <td class="act">${h.pending ? "" : `<button class="btn gray sm" type="button" data-unhide="${esc(h.key)}" data-fk="u:${esc(h.key)}" aria-label="${esc(h.name || h.key)} wieder anzeigen">${icon("eye")} Wieder anzeigen</button>`}</td></tr>`;
}
// svcRows: the rows of a section under group headings ("System 5"), each heading with a checkbox for its group.
function svcRows(list, rowFn, selectable) {
  return groupBy(svcSort(list), (c) => c.group || "Weitere").map(([g, cs]) => {
    const bad = cs.filter((c) => c.state === "crit" || c.state === "warn").length;
    return `<tr class="sv-gh"><td class="sv-sel">${selectable ? selBox("data-selg", `Alle in ${g} auswählen`) : ""}</td><td colspan="6"><span class="sv-gname">${esc(g)}</span> <span class="muted">${cs.length}</span>${bad ? " " + pill(`${bad} auffällig`, cs.some((c) => c.state === "crit") ? "rose" : "amber") : ""}</td></tr>` + cs.map(rowFn).join("");
  }).join("");
}
function svcSection({ id, title, ic, count, sub = "", rows, open = true, selectable = true, extra = "", cls = "", attrs = "" }) {
  return `<details class="panel flush sp sv-sec ${cls}" data-keep="sv:${esc(id)}" ${attrs} ${open ? "open" : ""}><summary><span class="sp-title">${icon(ic)}<span>${title}</span> <span class="sv-n">(${count})</span></span>${sub ? `<span class="muted small sv-sub">${sub}</span>` : ""}${icon("chevron-down", "sp-chev")}</summary>${extra}
    <div class="table-wrap"><table class="sv-table"><thead><tr><th class="sv-sel">${selectable ? selBox("data-sela", `Alle in ${title} auswählen`) : ""}</th><th>Zustand</th><th>Dienst</th><th>Zusammenfassung</th><th class="spark-h" title="Verlauf der letzten 6 Stunden">6 Std.</th><th>Seit</th><th><span class="sr-only">Aktionen</span></th></tr></thead>
    <tbody>${rows}<tr class="sv-none" hidden><td colspan="7" class="muted small">Keine Treffer.</td></tr></tbody></table></div></details>`;
}
// svcAgentSection: an attached agent with its checks (they open the agent's own check pages; its
// rules are changed on the agent's page, so the rows have no checkbox and no mode button).
function svcAgentSection(a) {
  const dv = a.device, st = a.status || {}, ag = dv.agent || {}, checks = st.checks || [];
  const s = devState({ ...dv, state: st.state });
  const meta = [
    dv.node && dv.name !== dv.node ? `<b>${esc(dv.name)}</b>` : "",
    ag.platform ? esc([ag.platform, ag.platform_version].filter(Boolean).join(" ")) : "",
    ag.version ? `Agent <span class="mono">${esc(ag.version)}</span>` : "",
    `letzter Kontakt <span title="${esc(fmtTime(st.last_poll, true))}">${esc(fmtAgo(st.last_poll))}</span>`,
    `<a href="/devices/${enc(dv.id)}" data-go>Details und Einstellungen</a>`,
  ].filter(Boolean).join(" · ");
  const extra = `<div class="att-meta t-sub">${meta}</div>${st.message && s !== "ok" ? `<div class="att-msg">${esc(st.message)}</div>` : ""}` +
    (checks.length ? "" : `<div class="att-empty muted small">${dv.enabled === false ? "Der Agent ist deaktiviert." : "Noch kein Bericht vom Agent – läuft der Dienst pk95agent auf dem Server?"}</div>`);
  return svcSection({ id: "att:" + dv.id, title: `Host ${esc(attLabel(dv))} · Agent ${statePill(s)}`, ic: "desktop", count: checks.length, rows: svcRows(checks, (c) => svcRow(c, dv.id, null, false), false),
    selectable: false, extra, cls: `att-group st-${s}`, attrs: `data-att="${esc(dv.id)}"` });
}
// svcList renders the device overview as a service list.
function svcList(dv, st, att = []) {
  const checks = st.checks || [], hid = st.hidden || [];
  if (!checks.length && !hid.length) return checksView(dv, st, att);
  const sel = svcSel.get(dv.id) || new Set();
  const known = new Set([...checks.map((c) => c.key), ...hid.filter((h) => !h.pending).map((h) => h.key)]);
  for (const k of [...sel]) if (!known.has(k)) sel.delete(k);
  svcSel.set(dv.id, sel);
  const f = svcFilter.get(dv.id) || { q: "", bad: false };
  const mon = checks.filter((c) => !isQuiet(c)), qu = checks.filter(isQuiet), off = hid.filter((h) => !h.pending).length;
  const nBad = checks.filter(svcBad).length + att.reduce((n, a) => n + ((a.status || {}).checks || []).filter(svcBad).length, 0);
  const row = (c) => svcRow(c, dv.id, sel, true);
  const tools = `<div class="sv-tools"><input type="search" data-svq data-fk="svq" value="${esc(f.q)}" placeholder="Dienste filtern …" aria-label="Dienste filtern" autocomplete="off">
    <div class="seg" role="group" aria-label="Nach Zustand filtern"><button type="button" data-svb="" data-fk="svb:0" aria-pressed="${!f.bad}">Alle</button><button type="button" data-svb="1" data-fk="svb:1" aria-pressed="${!!f.bad}">Auffällig${nBad ? ` (${nBad})` : ""}</button></div></div>`;
  const secs = [
    mon.length ? svcSection({ id: "alert", title: "Überwachte Dienste", ic: "bell", count: mon.length, sub: "lösen bei Problemen Alarme aus", rows: svcRows(mon, row, true) }) : "",
    qu.length ? svcSection({ id: "quiet", title: "Nur Anzeige", ic: "eye", count: qu.length, sub: "werden aufgezeichnet, lösen aber keinen Alarm aus", rows: svcRows(qu, row, true) }) : "",
    ...att.map(svcAgentSection),
    hid.length ? svcSection({ id: "off", title: "Ausgeblendet", ic: "eye-off", count: off, sub: "werden nicht angezeigt und nicht aufgezeichnet", rows: svcRows(hid, (h) => svcHiddenRow(h, sel), true), open: false }) : "",
  ].join("");
  const bulk = `<div class="sv-bulk" data-sv-bulk role="region" aria-label="Ausgewählte Dienste" hidden><span class="sv-bulk-n" data-sv-n aria-live="polite"></span>
    ${RULE_MODES.map(([m, , ic, sub]) => `<button class="btn ${m === "alert" ? "" : "gray"} sm" type="button" data-bulk="${m}" data-fk="b:${m}" title="${esc(sub)}">${icon(ic)} ${esc(RULE_MODE_SHORT[m])}</button>`).join("")}
    <button class="btn ghost sm" type="button" data-bulk-clear data-fk="b:clear">Auswahl aufheben</button></div>`;
  return ckSummary(st, att, "list") + dvNotes(dv) + tools + secs + bulk;
}
// applySvFilter shows the rows matching the search words (all of them, in name, key, group or
// summary) and, with "Auffällig", only the checks that need a look; empty group headings hide.
function applySvFilter(root, f) {
  const words = lc(f.q).trim().split(/\s+/).filter(Boolean);
  qsa(".sv-sec", root).forEach((sec) => {
    let any = 0, gh = null, n = 0;
    const close = () => { if (gh) gh.hidden = n === 0; };
    qsa("tbody > tr", sec).forEach((tr) => {
      if (tr.classList.contains("sv-gh")) { close(); gh = tr; n = 0; return; }
      if (!tr.classList.contains("sv-row")) return;
      const show = words.every((w) => tr.dataset.q.includes(w)) && (!f.bad || tr.dataset.bad === "1");
      tr.hidden = !show;
      if (show) { n++; any++; }
    });
    close();
    const none = qs("tr.sv-none", sec);
    if (none) none.hidden = any > 0;
  });
}
// groupRowsOf: the check rows under a group heading (up to the next heading).
function groupRowsOf(gh) {
  const out = [];
  for (let tr = gh.nextElementSibling; tr && tr.classList.contains("sv-row"); tr = tr.nextElementSibling) out.push(tr);
  return out;
}
// syncSel brings checkboxes, headings and the action bar in line with the checked keys.
function syncSel(root, sel) {
  const boxOf = (tr) => qs("input[data-sel]", tr);
  qsa("tr.sv-row", root).forEach((tr) => { const b = boxOf(tr); if (b) { b.checked = sel.has(b.dataset.sel); tr.classList.toggle("sel", b.checked); } });
  const mark = (box, rows) => {
    const bs = rows.filter((tr) => !tr.hidden).map(boxOf).filter(Boolean), on = bs.filter((b) => b.checked).length;
    box.checked = bs.length > 0 && on === bs.length;
    box.indeterminate = on > 0 && on < bs.length;
    box.disabled = bs.length === 0;
  };
  qsa("input[data-selg]", root).forEach((b) => mark(b, groupRowsOf(b.closest("tr"))));
  qsa("input[data-sela]", root).forEach((b) => mark(b, qsa("tr.sv-row", b.closest("table"))));
  const bar = qs("[data-sv-bulk]", root);
  if (bar) {
    bar.hidden = sel.size === 0;
    qs("[data-sv-n]", bar).textContent = `${sel.size} ${sel.size === 1 ? "Dienst" : "Dienste"} ausgewählt:`;
  }
}

// ---------- alerts table (alerts page and device tab) ----------
function alertsTable(list, opts) {
  const now = nowSec();
  return `<div class="panel flush"><div class="table-wrap"><table class="resp"><thead><tr><th>Schwere</th>${opts.device ? "<th>Gerät</th>" : ""}${opts.check !== false ? "<th>Check</th>" : ""}<th>Meldung</th><th>Beginn</th><th>Ende / Dauer</th><th>Quittiert</th><th></th></tr></thead><tbody>
    ${list.map((a) => `<tr class="${a.ended ? "off" : ""}">
      ${td("Schwere", sevPill(a.severity))}
      ${opts.device ? td("Gerät", `<a class="t-main" href="/devices/${enc(a.device_id)}" data-go>${esc(a.device_name)}</a>`) : ""}
      ${opts.check !== false ? td("Check", `<div class="t-main">${esc(a.check_name)}</div><div class="t-sub mono">${esc(a.check_key)}</div>`) : ""}
      ${td("Meldung", `<span class="small">${esc(a.message || "")}</span>`)}
      ${td("Beginn", `<span class="nowrap">${esc(fmtTime(a.started, true))}</span><div class="t-sub">${esc(fmtAgo(a.started))}</div>`)}
      ${td("Ende / Dauer", a.ended ? `<span class="nowrap">${esc(fmtTime(a.ended, true))}</span><div class="t-sub">Dauer ${esc(fmtUptime(a.ended - a.started))}</div>` : `${pill("aktiv", a.severity === "crit" ? "rose" : "amber", true, true)}<div class="t-sub">seit ${esc(fmtUptime(now - a.started))}</div>`)}
      ${td("Quittiert", a.acked_by ? `${esc(a.acked_by)}<div class="t-sub">${esc(fmtTime(a.acked_at, true))}</div>` : '<span class="muted">—</span>', "small")}
      <td class="act">${!a.ended && !a.acked_by ? `<button class="btn gray sm" type="button" data-ack="${esc(a.id)}">${icon("check")} Quittieren</button>` : ""}</td></tr>`).join("")}
  </tbody></table></div></div>`;
}

// alertsBlock loads alerts with paging ("Mehr laden" uses before=<id of the last alert>).
async function alertsBlock(box, query, opts, alive) {
  const LIMIT = 50;
  let list = [], more = false;
  const load = async (append) => {
    const before = append && list.length ? list[list.length - 1].id : 0;
    const r = await GET(`/api/alerts?${query}&limit=${LIMIT}${before ? `&before=${enc(before)}` : ""}`);
    if (!alive()) return;
    const got = r.alerts || [];
    list = append ? list.concat(got) : got;
    more = r.more !== undefined ? !!r.more : got.length >= LIMIT;
    box.innerHTML = list.length ? alertsTable(list, opts) + (more ? `<div style="text-align:center;margin:12px"><button class="btn gray" type="button" data-more>Mehr laden</button></div>` : "")
      : empty(opts.emptyTitle, opts.emptyText || "", "", "bell");
    const m = qs("[data-more]", box);
    if (m) m.onclick = () => run(m, () => load(true));
    bindAck(box, () => load(false));
  };
  if (!box.innerHTML.trim()) box.innerHTML = loadingPanel();
  await load(false);
  return load;
}

// ---------- thresholds editor (device settings and global settings) ----------
function splitMetric(s) { const i = String(s || "").indexOf(":"); return i < 0 ? [s || "", ""] : [s.slice(0, i), s.slice(i + 1)]; }
function thrText(t) {
  if (!t) return "";
  const [n] = splitMetric(t.metric), u = metricUnitOf(n), op = t.below ? "≤" : "≥";
  const parts = [];
  if (t.disabled) return "aus";
  if (isNum(t.warn)) parts.push(`Warnung ${op} ${fmtVal(t.warn, u)}`);
  if (isNum(t.crit)) parts.push(`Kritisch ${op} ${fmtVal(t.crit, u)}`);
  if (t.count > 1) parts.push(`nach ${t.count} Werten`);
  return parts.join(" · ") || "keine Grenze";
}
function findThr(list, metric) {
  const [n] = splitMetric(metric);
  return (list || []).find((t) => t.metric === metric) || (list || []).find((t) => t.metric === n) || null;
}

// thresholdEditor: rows {metric (name[:instance]), warn, crit, below, count, disabled}; effective(metric) returns the fallback.
function thresholdEditor(box, rows, effective, fallbackText) {
  let list = (rows || []).map((t) => ({ ...t }));
  const names = Object.keys(METRIC_LABELS);
  const row = (t, i) => {
    const [n, inst] = splitMetric(t.metric);
    const opts = (names.includes(n) || !n ? names : [n, ...names]).map((k) => `<option value="${esc(k)}" ${k === n ? "selected" : ""}>${esc(metricLabel(k))} (${esc(k)})</option>`).join("");
    const eff = effective(t.metric || n);
    const unit = metricUnitOf(n);
    const numF = (k, label, v) => `<div class="field" data-field="thresholds.${i}.${k}"><label class="fl" for="th-${i}-${k}">${label}${unit ? ` (${esc(unit)})` : ""}</label><input type="number" step="any" id="th-${i}-${k}" data-k="${k}" value="${isNum(v) ? esc(v) : ""}" placeholder="${eff && isNum(eff[k]) ? esc(eff[k]) : "—"}"><span class="err" hidden></span></div>`;
    return `<div class="ed-row th-row" data-i="${i}"><div class="ed-grid th-grid">
        <div class="field span2" data-field="thresholds.${i}.metric"><label class="fl" for="th-${i}-name">Messwert</label><select id="th-${i}-name" data-k="name">${opts}</select><span class="err" hidden></span></div>
        <div class="field" data-field="thresholds.${i}.instance"><label class="fl" for="th-${i}-inst">Instanz</label><input type="text" id="th-${i}-inst" data-k="inst" class="mono" value="${esc(inst)}" placeholder="alle" autocomplete="off" spellcheck="false"><span class="err" hidden></span></div>
        ${numF("warn", "Warnung", t.warn)}${numF("crit", "Kritisch", t.crit)}
        <div class="field" data-field="thresholds.${i}.count"><label class="fl" for="th-${i}-count">Anzahl</label><input type="number" min="1" id="th-${i}-count" data-k="count" value="${t.count > 1 ? esc(t.count) : ""}" placeholder="${eff && eff.count > 1 ? esc(eff.count) : "1"}"><span class="err" hidden></span></div>
        <div class="field cbf span2 th-checks" data-field="thresholds.${i}.below"><label class="check"><input type="checkbox" data-k="below" ${t.below ? "checked" : ""}><span>unterhalb</span></label><label class="check"><input type="checkbox" data-k="disabled" ${t.disabled ? "checked" : ""}><span>deaktiviert</span></label><span class="err" hidden></span></div>
      </div><div class="ed-actions"><button class="ib red" type="button" data-th-del title="Entfernen" aria-label="Schwellwert entfernen">${icon("trash")}</button></div>
      <div class="th-def t-sub">${eff ? `${esc(fallbackText)}: ${esc(thrText(eff))}` : "Ohne Standardwert – nur diese Grenze gilt."}</div></div>`;
  };
  const sync = () => {
    list = qsa(".th-row", box).map((el) => {
      const g = (k) => { const x = qs(`[data-k="${k}"]`, el); return x.type === "checkbox" ? x.checked : x.value.trim(); };
      const num = (k) => { const v = g(k).replace(",", "."); return v === "" || !isFinite(v) ? undefined : Number(v); };
      const inst = g("inst");
      const t = { metric: g("name") + (inst ? ":" + inst : "") };
      const w = num("warn"), c = num("crit"), cnt = num("count");
      if (w !== undefined) t.warn = w;
      if (c !== undefined) t.crit = c;
      if (g("below")) t.below = true;
      if (cnt > 1) t.count = Math.round(cnt);
      if (g("disabled")) t.disabled = true;
      return t;
    });
  };
  const draw = () => {
    box.innerHTML = (list.length ? `<div class="ed-rows">${list.map(row).join("")}</div>` : `<div class="muted small">Keine eigenen Schwellwerte – es gelten die Standardwerte.</div>`) +
      `<div class="mt"><button class="btn gray sm" type="button" data-th-add>${icon("plus")} Schwellwert hinzufügen</button></div>`;
    qsa(".th-row", box).forEach((el) => {
      qs("[data-th-del]", el).onclick = () => { sync(); list.splice(+el.dataset.i, 1); draw(); };
      qs('[data-k="name"]', el).onchange = () => {
        sync();
        const i = +el.dataset.i, eff = effective(list[i].metric);
        if (eff) { if (eff.below) list[i].below = true; else delete list[i].below; }
        draw();
      };
    });
    qs("[data-th-add]", box).onclick = () => {
      sync();
      const used = new Set(list.map((t) => t.metric));
      const n = ["disk.usage", "cpu.usage", "mem.usage", "ping.rtt"].find((x) => !used.has(x)) || "disk.usage";
      const eff = effective(n);
      list.push({ metric: n, below: eff ? eff.below : false });
      draw();
      const rows = qsa(".th-row", box);
      qs('[data-k="name"]', rows[rows.length - 1]).focus();
    };
  };
  draw();
  return { read() { sync(); return list.filter((t) => t.metric); }, set(r) { list = (r || []).map((t) => ({ ...t })); draw(); } };
}

// ---------- check rules editor (device settings) ----------
// rulesEditor: rows {match, mode}; known = checks of the device [{key, name, group}] for the
// suggestions and the "trifft …" line under each row.
function rulesEditor(box, rules, known) {
  let list = (rules || []).map((r) => ({ match: r.match || "", mode: normMode(r.mode) }));
  const groups = groupBy(known.filter((c) => c.key !== REACH_KEY), (c) => c.group || "Weitere");
  const sugg = [];
  for (const [g, cs] of groups) {
    const keys = cs.map((c) => c.key), outside = known.filter((c) => (c.group || "Weitere") !== g).map((c) => c.key);
    for (const p of groupPlan(keys, outside).patterns) if (!sugg.some((s) => s[0] === p)) sugg.push([p, `Alle: ${g}`]);
  }
  const dl = `<datalist id="dl-rule-keys">${sugg.map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`).join("")}${[...known].filter((c) => c.key !== REACH_KEY).sort(byName).map((c) => `<option value="${esc(c.key)}">${esc(c.name || c.key)}</option>`).join("")}</datalist>`;
  const hits = (match) => {
    const m = String(match || "").trim();
    if (!m) return "";
    if (m === REACH_KEY) return "Die Erreichbarkeit wird immer überwacht – diese Regel wirkt nicht.";
    const hit = known.filter((c) => c.key !== REACH_KEY && (c.key === m || (m.includes("*") && globMatch(m, c.key))));
    if (!hit.length) return m.includes("*") ? "Trifft zurzeit keinen Check." : "Kein Check mit diesem Key bekannt (gilt, sobald er auftaucht).";
    const names = hit.slice(0, 4).map((c) => c.name || c.key).join(", ");
    if (hit.length === 1 && !m.includes("*")) return `Check: ${names}`;
    // checks of this pattern that an exact rule or a longer pattern decides instead
    const trimmed = list.map((r) => ({ ...r, match: String(r.match || "").trim() }));
    const other = hit.filter((c) => { const w = ruleFor(trimmed, c.key); return w && w.match !== m; }).length;
    return `Trifft ${hit.length} ${hit.length === 1 ? "Check" : "Checks"}: ${names}${hit.length > 4 ? " …" : ""}${other ? ` (${other} davon durch eine andere Regel bestimmt)` : ""}`;
  };
  const row = (r, i) => `<div class="ed-row ru-row" data-i="${i}"><div class="ed-grid ru-grid">
      <div class="field" data-field="rules.${i}.match"><label class="fl" for="ru-${i}-match">Muster / Check-Key</label><input type="text" id="ru-${i}-match" data-k="match" class="mono" list="dl-rule-keys" value="${esc(r.match)}" placeholder="z. B. snmp:if:* oder pve:vm:105" autocomplete="off" spellcheck="false"><span class="err" hidden></span></div>
      <div class="field" data-field="rules.${i}.mode"><label class="fl" for="ru-${i}-mode">Modus</label><select id="ru-${i}-mode" data-k="mode">${RULE_MODES.map(([m, l]) => `<option value="${m}" ${m === r.mode ? "selected" : ""}>${esc(m === "quiet" ? "Nur anzeigen" : l)}</option>`).join("")}</select><span class="err" hidden></span></div>
    </div><div class="ed-actions"><button class="ib red" type="button" data-ru-del title="Entfernen" aria-label="Regel entfernen">${icon("trash")}</button></div>
    <div class="ru-hit t-sub" data-ru-hit>${esc(hits(r.match))}</div></div>`;
  const sync = () => {
    list = qsa(".ru-row", box).map((el) => ({ match: qs('[data-k="match"]', el).value.trim(), mode: qs('[data-k="mode"]', el).value }));
  };
  const draw = () => {
    box.innerHTML = dl + (list.length ? `<div class="ed-rows">${list.map(row).join("")}</div>` : `<div class="muted small">Keine Regeln – alle Checks werden überwacht.</div>`) +
      `<div class="mt"><button class="btn gray sm" type="button" data-ru-add>${icon("plus")} Regel hinzufügen</button></div>`;
    qsa(".ru-row", box).forEach((el) => {
      qs("[data-ru-del]", el).onclick = () => { sync(); list.splice(+el.dataset.i, 1); draw(); const a = qs("[data-ru-add]", box); if (a) a.focus(); };
      const inp = qs('[data-k="match"]', el);
      // every hit line can change (an exact rule takes checks away from a pattern row)
      inp.oninput = () => { sync(); qsa(".ru-row", box).forEach((r) => { qs("[data-ru-hit]", r).textContent = hits(list[+r.dataset.i].match); }); };
    });
    qs("[data-ru-add]", box).onclick = () => {
      sync();
      list.push({ match: "", mode: "quiet" });
      draw();
      const rows = qsa(".ru-row", box);
      qs('[data-k="match"]', rows[rows.length - 1]).focus();
    };
  };
  draw();
  // every row is sent (also an empty one), so the field errors "rules.N.*" land at the right row
  return { read() { sync(); return list; } };
}

// ---------- device settings tab ----------
async function deviceSettingsTab(box, dv, alive, reload, st = {}) {
  box.innerHTML = loadingPanel();
  const set = await GET("/api/settings");
  if (!alive()) return;
  const global = set.thresholds || [], defs = set.defaults || [];
  const ag = dv.agent || {};
  const win = ag.os === "windows";
  box.innerHTML = `<form class="panel form" data-dset novalidate>
      <div><div class="panel-title">Schwellwerte für dieses Gerät</div>
      <p class="panel-intro" style="margin:4px 0 0">Überschreiben die globalen Schwellwerte (Einstellungen → Schwellwerte). Mit „Instanz“ gilt eine Grenze nur für eine Platte, einen Port oder einen Gast (z. B. <code>/var</code>, <code>C:</code>, <code>vm/100</code>). „Unterhalb“: schlecht, wenn der Wert kleiner ist (Akku, Toner, Zertifikat). „Anzahl“: so viele Werte in Folge, bevor der Zustand wechselt.</p></div>
      <div class="form-error" hidden></div>
      <div data-th></div>
      <div class="section"><div class="section-title">Überwachung – Regeln</div>
        <p class="hint ru-help">Lege fest, was überwacht wird. <b>Überwachen</b>: der Zustand zählt und löst Alarme aus (Standard). <b>Nur anzeigen</b>: Werte werden aufgezeichnet und angezeigt, aber nie als Warnung oder kritisch gewertet. <b>Ausblenden</b>: der Check wird verworfen, es werden keine Werte gespeichert.
          Muster: <code>*</code> steht für beliebige Zeichen, z. B. <code>snmp:if:*</code> (alle Ports) oder <code>pve:vm:*</code> (alle VMs). Ein exakter Check-Key gilt vor jedem Muster, unter mehreren passenden Mustern gewinnt das längere. Die Erreichbarkeit wird immer überwacht.</p>
        <div data-rules></div></div>
      ${dv.type === "agent" ? `<div class="section"><div class="section-title">Dienste</div><div class="form-grid">
        ${field({ name: "services", label: "Diese Dienste müssen laufen", type: "textarea", mono: true, rows: 4, value: (ag.services || []).join("\n"), wide: true, hint: win ? "Dienstnamen (nicht Anzeigenamen), einer pro Zeile – z. B. Spooler, MSSQLSERVER, W3SVC." : "systemd-Units, eine pro Zeile – z. B. nginx.service, postgresql, docker." })}
        ${win ? field({ name: "watch_auto_services", label: "Alle automatisch startenden Dienste überwachen", type: "checkbox", value: ag.watch_auto_services, wide: true, hint: "Jeder Dienst mit Starttyp „Automatisch“ muss laufen – Ausnahmen unten eintragen." }) : ""}
        ${field({ name: "ignore_services", label: "Diese Dienste ignorieren", type: "textarea", mono: true, rows: 3, value: (ag.ignore_services || []).join("\n"), wide: true, hint: "Werden nie gemeldet, auch nicht über die automatische Überwachung." })}
      </div></div>` : ""}
      <div class="flex"><button class="btn" type="submit">Speichern</button><span class="muted small">Gilt ab der nächsten Abfrage${dv.type === "agent" ? " bzw. dem nächsten Bericht des Agents" : ""}.</span></div>
    </form>`;
  const form = qs("[data-dset]", box);
  const effective = (m) => findThr(global, m) || findThr(defs, m);
  const ed = thresholdEditor(qs("[data-th]", form), dv.thresholds, effective, "Sonst gilt");
  const known = [...new Map([...(st.checks || []), ...(st.hidden || [])].map((c) => [c.key, { key: c.key, name: c.name, group: c.group }])).values()];
  const ru = rulesEditor(qs("[data-rules]", form), dv.rules, known);
  form.onsubmit = async (e) => {
    e.preventDefault();
    const btn = qs('button[type="submit"]', form);
    btn.disabled = true;
    try {
      showErrors(form, null);
      const cur = (await GET(`/api/devices/${enc(dv.id)}`)).device;
      cur.thresholds = ed.read();
      cur.rules = ru.read();
      if (dv.type === "agent") {
        const v = formValues(form), lines = (s) => String(s || "").split(/[\n,]+/).map((x) => x.trim()).filter(Boolean);
        cur.agent = { ...(cur.agent || {}), services: lines(v.services), watch_auto_services: !!v.watch_auto_services, ignore_services: lines(v.ignore_services) };
      }
      await PUT(`/api/devices/${enc(dv.id)}`, cur);
      toast("Gespeichert.");
      await reload();
    } catch (err) { showErrors(form, stripErr(err, "agent")); }
    finally { btn.disabled = false; }
  };
}

// ---------- charts (hand-written SVG) ----------
const RANGES = [["1h", "1 Std."], ["6h", "6 Std."], ["24h", "24 Std."], ["7d", "7 Tage"], ["30d", "30 Tage"], ["1y", "1 Jahr"]];
const RANGE_SECS = { "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000, "1y": 31536000 };
const DEFAULT_SERIES = ["cpu.usage", "mem.usage", "disk.usage", "swap.usage", "net.rx", "net.tx", "if.rx", "if.tx", "storage.usage", "ping.rtt", "ping.loss", "http.time", "tcp.time", "dns.time", "cert.days", "ups.battery", "ups.load", "ups.runtime", "ups.power", "poe.power", "poe.usage", "printer.toner", "temp", "custom"];
const N_COLORS = 6;
const MON = ["Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"];

// defaultSelection picks the usual suspects; for many instances (switch ports, guests) only the busiest.
const MAX_CHART_SERIES = 16; // per /metrics request, see maxChartSeries in internal/api/devices.go

// Series carry uid "<device id>:<series id>" (a parent also lists its attached agents' series).
function defaultSelection(series) {
  const pick = series.filter((s) => DEFAULT_SERIES.includes(s.name));
  const out = new Set();
  const byN = groupBy(pick, (s) => s.name);
  const ifScore = {}, ik = (s) => (s.dev || "") + "|" + s.instance;
  for (const s of pick) if (s.name === "if.rx" || s.name === "if.tx" || s.name === "net.rx" || s.name === "net.tx") ifScore[ik(s)] = (ifScore[ik(s)] || 0) + (s.last_value || 0);
  const topIf = new Set(Object.entries(ifScore).sort((a, b) => b[1] - a[1]).slice(0, 4).map(([k]) => k));
  for (const [n, list] of byN) {
    if (/^(if|net)\.(rx|tx)$/.test(n)) list.filter((s) => topIf.has(ik(s))).forEach((s) => out.add(s.uid));
    else [...list].sort((a, b) => (b.last_value || 0) - (a.last_value || 0)).slice(0, 8).forEach((s) => out.add(s.uid));
  }
  if (!out.size) series.slice(0, 4).forEach((s) => out.add(s.uid));
  return out;
}

function niceTicks(lo, hi, n, unit, minBase = 1) {
  if (!(hi > lo)) hi = lo + 1;
  const bytes = unit === "B" || unit === "B/s";
  let base = minBase;
  if (bytes) while (hi / base >= 1024 && base < 1024 ** 5) base *= 1024;
  if (unit === "s") base = hi >= 2 * 86400 ? 86400 : hi >= 7200 ? 3600 : hi >= 120 ? 60 : 1;
  const l = lo / base, h = hi / base;
  const raw = (h - l) / n;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const r = raw / mag;
  const step = (r <= 1 ? 1 : r <= 2 ? 2 : r <= 2.5 ? 2.5 : r <= 5 ? 5 : 10) * mag;
  const start = Math.floor(l / step + 1e-9) * step, end = Math.ceil(h / step - 1e-9) * step;
  const ticks = [];
  for (let v = start; v <= end + step / 2 && ticks.length < 20; v += step) ticks.push(+(v * base).toPrecision(12));
  if (ticks.length < 2) ticks.push(+((start + step) * base).toPrecision(12));
  // Rounding up can push the top tick into the next byte unit (e.g. 1000 KiB → 1500 KiB, labelled in MiB
  // as "1,4648"); pick the ticks again in that unit so all labels are round numbers.
  if (bytes && ticks[ticks.length - 1] / base >= 1024 && base < 1024 ** 5) return niceTicks(lo, hi, n, unit, base * 1024);
  return ticks;
}
// fmtAxis labels a y-axis tick. All ticks of an axis share one byte/time unit (picked from the
// largest tick) and as many decimals as the tick step needs, so labels never repeat or round off.
function fmtAxis(v, unit, step = 0, max = Math.abs(v)) {
  const dec = (x) => { for (let d = 0; d < 4; d++) { const y = x * 10 ** d; if (Math.abs(y - Math.round(y)) < 1e-6 * Math.max(1, y)) return d; } return 4; };
  const num = (x, div) => Number(x / div).toLocaleString("de-DE", { minimumFractionDigits: 0, maximumFractionDigits: dec(step / div) });
  if (unit === "B" || unit === "B/s") {
    const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
    let i = 0, b = 1;
    while (max / b >= 1024 && i < u.length - 1) { b *= 1024; i++; }
    return num(v, b) + " " + u[i] + (unit === "B/s" ? "/s" : "");
  }
  if (unit === "s") {
    const div = [86400, 3600, 60, 1].find((x) => step >= x && Math.abs(step / x - Math.round(step / x)) < 1e-6) || 1;
    const l = { 86400: " T.", 3600: " Std.", 60: " Min.", 1: " s" }[div];
    return num(v, div) + l;
  }
  if (unit === "d") return num(v, 1) + " T.";
  if (unit === "min") return num(v, 1) + " Min.";
  if (unit === "rpm") return num(v, 1) + NNBSP + "U/min";
  if (unit === "1/s") return num(v, 1) + "/s";
  return num(v, 1) + (unit ? NNBSP + unit : "");
}
const T_STEPS = [60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800, 1209600, 2592000, 7776000];
function timeTicks(from, to, max) {
  const span = to - from;
  const st = T_STEPS.find((s) => span / s <= max) || T_STEPS[T_STEPS.length - 1];
  const out = [];
  if (st >= 2592000) {
    const months = Math.round(st / 2592000), d0 = new Date(from * 1000);
    let d = new Date(d0.getFullYear(), d0.getMonth() + 1, 1);
    while (d.getTime() / 1000 <= to && out.length < 40) { out.push(d.getTime() / 1000); d = new Date(d.getFullYear(), d.getMonth() + months, 1); }
    return { ticks: out, st };
  }
  const tz = new Date(from * 1000).getTimezoneOffset() * 60; // local = utc - tz
  for (let t = Math.ceil((from - tz) / st) * st + tz; t <= to && out.length < 60; t += st) out.push(t);
  return { ticks: out, st };
}
function timeLabel(t, st, span) {
  const d = new Date(t * 1000), p = (x) => String(x).padStart(2, "0");
  if (st >= 2592000) return `${MON[d.getMonth()]} ${String(d.getFullYear()).slice(2)}`;
  if (st >= 86400) return `${p(d.getDate())}.${p(d.getMonth() + 1)}.`;
  if (d.getHours() === 0 && d.getMinutes() === 0 && span > 6 * 3600) return `${p(d.getDate())}.${p(d.getMonth() + 1)}.`;
  return `${p(d.getHours())}:${p(d.getMinutes())}`;
}
// nearest returns the point closest to t (points are sorted by time).
function nearest(points, t) {
  let lo = 0, hi = points.length - 1;
  if (hi < 0) return null;
  while (hi - lo > 1) { const m = (lo + hi) >> 1; if (points[m][0] < t) lo = m; else hi = m; }
  return Math.abs(points[lo][0] - t) <= Math.abs(points[hi][0] - t) ? points[lo] : points[hi];
}

// chartCls is the colour class of series i of a chart; a series with dash (a total next to the
// used value) is a gray dashed line.
const chartCls = (list, i) => (list[i] && list[i].dash ? "sd" : `s${i % N_COLORS}`);
// drawChart renders one chart (several series of one metric) into box and binds the hover crosshair.
// Series points: [ts, avg, min, max]; null avg or a jump in time is drawn as a gap.
function drawChart(box, list, unit, from, to, step) {
  const W = Math.max(280, Math.round(box.clientWidth || 600)), H = W < 520 ? 190 : 230;
  const PL = 62, PR = 12, PT = 10, PB = 24;
  const iw = W - PL - PR, ih = H - PT - PB;
  const pts = list.map((s) => (s.points || []).filter((p) => p && isNum(p[0])).sort((a, b) => a[0] - b[0]));
  let lo = Infinity, hi = -Infinity;
  pts.forEach((ps) => ps.forEach((p) => {
    if (!isNum(p[1])) return;
    lo = Math.min(lo, p[1], isNum(p[2]) ? p[2] : p[1]);
    hi = Math.max(hi, p[1], isNum(p[3]) ? p[3] : p[1]);
  }));
  if (!isFinite(lo)) { lo = 0; hi = 1; }
  if (lo >= 0 && unit !== "V" && unit !== "°C") lo = 0;
  if (unit === "%" && hi <= 100 && lo >= 0) hi = 100;
  if (hi <= lo) hi = lo + (Math.abs(lo) || 1);
  const ticks = niceTicks(lo, hi, ih < 170 ? 4 : 5, unit);
  const y0 = ticks[0], y1 = ticks[ticks.length - 1];
  if (!(to > from)) to = from + 1;
  const X = (t) => PL + ((t - from) / (to - from)) * iw;
  const Y = (v) => PT + ih - ((v - y0) / (y1 - y0)) * ih;
  const f1 = (n) => n.toFixed(1);
  const gapAfter = Math.max((step || 60) * 2.5, 1);
  let s = `<svg class="chart" viewBox="0 0 ${W} ${H}" width="100%" height="${H}" preserveAspectRatio="none" aria-hidden="true">`;
  const tStep = ticks[1] - ticks[0], tMax = Math.max(Math.abs(y0), Math.abs(y1));
  ticks.forEach((v) => { const y = f1(Y(v)); s += `<line class="gl" x1="${PL}" x2="${W - PR}" y1="${y}" y2="${y}"/><text class="ax" x="${PL - 6}" y="${(+y + 3.5).toFixed(1)}" text-anchor="end">${esc(fmtAxis(v, unit, tStep, tMax))}</text>`; });
  const tt = timeTicks(from, to, Math.max(2, Math.floor(iw / 84)));
  tt.ticks.forEach((t) => {
    const x = X(t);
    if (x < PL - 0.5 || x > W - PR + 0.5) return;
    s += `<line class="gl gx" x1="${f1(x)}" x2="${f1(x)}" y1="${PT}" y2="${PT + ih}"/><text class="ax" x="${f1(Math.min(Math.max(x, PL + 14), W - PR - 14))}" y="${H - 6}" text-anchor="middle">${esc(timeLabel(t, tt.st, to - from))}</text>`;
  });
  pts.forEach((ps, i) => {
    const segs = [];
    let cur = [];
    ps.forEach((p, k) => {
      if (!isNum(p[1])) { if (cur.length) segs.push(cur); cur = []; return; }
      if (cur.length && p[0] - ps[k - 1][0] > gapAfter) { segs.push(cur); cur = []; }
      cur.push(p);
    });
    if (cur.length) segs.push(cur);
    const c = chartCls(list, i), dash = !!list[i].dash;
    segs.forEach((g) => {
      const band = !dash && g.some((p) => isNum(p[2]) && isNum(p[3]) && p[2] !== p[3]);
      if (band && g.length > 1) {
        const up = g.map((p) => `${f1(X(p[0]))},${f1(Y(isNum(p[3]) ? p[3] : p[1]))}`), dn = g.slice().reverse().map((p) => `${f1(X(p[0]))},${f1(Y(isNum(p[2]) ? p[2] : p[1]))}`);
        s += `<polygon class="bd ${c}" points="${up.concat(dn).join(" ")}"/>`;
      }
      if (g.length === 1) s += `<circle class="dt ${c}" cx="${f1(X(g[0][0]))}" cy="${f1(Y(g[0][1]))}" r="2"/>`;
      else s += `<path class="ln ${c}${dash ? " dash" : ""}" d="M${g.map((p) => `${f1(X(p[0]))} ${f1(Y(p[1]))}`).join("L")}"/>`;
    });
  });
  s += `<line class="xh" data-xh x1="-10" x2="-10" y1="${PT}" y2="${PT + ih}" visibility="hidden"/><g data-dots></g></svg><div class="chart-tip" hidden></div>`;
  box.innerHTML = s;
  const svg = qs("svg", box), tip = qs(".chart-tip", box), xh = qs("[data-xh]", box), dots = qs("[data-dots]", box);
  let cur = null;
  const hide = () => { tip.hidden = true; xh.setAttribute("visibility", "hidden"); dots.innerHTML = ""; };
  const show = (t) => {
    t = Math.max(from, Math.min(to, t));
    const rows = pts.map((ps, i) => { const p = nearest(ps, t); return p && Math.abs(p[0] - t) <= gapAfter && isNum(p[1]) ? { i, p } : null; }).filter(Boolean);
    if (!rows.length) { hide(); return; }
    const at = rows.reduce((a, r) => (Math.abs(r.p[0] - t) < Math.abs(a - t) ? r.p[0] : a), rows[0].p[0]);
    cur = at;
    const x = X(at);
    xh.setAttribute("x1", f1(x)); xh.setAttribute("x2", f1(x)); xh.setAttribute("visibility", "visible");
    dots.innerHTML = rows.map((r) => `<circle class="pt ${chartCls(list, r.i)}" cx="${f1(X(r.p[0]))}" cy="${f1(Y(r.p[1]))}" r="3.5"/>`).join("");
    const shown = rows.slice().sort((a, b) => b.p[1] - a.p[1]);
    tip.innerHTML = `<div class="tt-time">${esc(fmtTime(at, true))}</div>` + shown.slice(0, 12).map((r) => {
      const sr = list[r.i], mm = isNum(r.p[2]) && isNum(r.p[3]) && r.p[2] !== r.p[3];
      return `<div class="tt-row"><i class="sw ${chartCls(list, r.i)}"></i><span class="tt-l">${esc(sr.label || sr.instance || metricLabel(sr.name))}</span><span class="v">${esc(fmtVal(r.p[1], unit))}</span></div>${mm ? `<div class="tt-sub">${esc(fmtVal(r.p[2], unit))} … ${esc(fmtVal(r.p[3], unit))}</div>` : ""}`;
    }).join("") + (shown.length > 12 ? `<div class="tt-sub">und ${shown.length - 12} weitere</div>` : "");
    tip.hidden = false;
    const scale = svg.getBoundingClientRect().width / W || 1, px = x * scale, bw = box.clientWidth, tw = tip.offsetWidth;
    tip.style.left = (px + 14 + tw > bw ? Math.max(0, px - 14 - tw) : px + 14) + "px";
    tip.style.top = Math.round(PT * scale) + "px";
  };
  svg.addEventListener("pointermove", (e) => {
    const r = svg.getBoundingClientRect();
    const x = ((e.clientX - r.left) * W) / r.width;
    if (x < PL || x > W - PR) { hide(); return; }
    show(from + ((x - PL) / iw) * (to - from));
  });
  svg.addEventListener("pointerleave", hide);
  box.onkeydown = (e) => {
    const st = step || (to - from) / 100;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") { e.preventDefault(); show((cur ?? to) + (e.key === "ArrowRight" ? st : -st)); }
    else if (e.key === "Home") { e.preventDefault(); show(from); }
    else if (e.key === "End") { e.preventDefault(); show(to); }
    else if (e.key === "Escape") hide();
  };
  box.onfocus = () => show(cur ?? to);
  box.onblur = hide;
}

// chartsTab: series picker and charts; att = attached agents [{device, status}] whose series a
// parent offers too (fetched with the agent's own id, labelled "<node> · …").
async function chartsTab(box, dv, st, alive, att = []) {
  box.innerHTML = loadingPanel();
  const srcs = [{ dev: dv.id, prefix: "", st }, ...att.map((a) => ({ dev: a.device.id, prefix: attLabel(a.device), st: a.status || {} }))];
  const res = await Promise.all(srcs.map((x, i) => GET(`/api/devices/${enc(x.dev)}/series`).catch((e) => { if (i === 0) throw e; return { series: [] }; })));
  if (!alive()) return;
  const series = [], names = {};
  srcs.forEach((src, i) => {
    const nm = Object.fromEntries((src.st.checks || []).map((c) => [c.key, c.name]));
    const list = (res[i].series || []).map((x) => ({ ...x, uid: `${src.dev}:${x.id}`, dev: src.dev, prefix: src.prefix, own: i === 0, gk: `${src.dev}|${x.check_key || ""}` }));
    list.forEach((x) => { names[x.gk] = (src.prefix ? src.prefix + " · " : "") + (nm[x.check_key] || x.check_key || "Weitere"); });
    list.sort((a, b) => String(names[a.gk]).localeCompare(String(names[b.gk]), "de", { numeric: true }) || metricLabel(a.name).localeCompare(metricLabel(b.name), "de") || String(a.instance).localeCompare(String(b.instance), "de", { numeric: true }));
    series.push(...list);
  });
  if (!series.length) {
    box.innerHTML = empty("Noch keine Messwerte", "Sobald das Gerät Werte liefert, stehen sie hier als Diagramm – bis zu einem Jahr zurück.", "", "chart");
    return;
  }
  const byUid = new Map(series.map((x) => [x.uid, x]));
  const skey = (x) => (x.own ? "" : "@" + x.dev + "|") + x.name + "|" + (x.instance || "");
  const selKey = "pk95mon-series:" + dv.id;
  let sel;
  try { const saved = JSON.parse(lsGet(selKey) || "null"); if (Array.isArray(saved)) sel = new Set(series.filter((x) => saved.includes(skey(x))).map((x) => x.uid)); } catch (e) { /* ignore */ }
  if (!sel || !sel.size) sel = defaultSelection(series);
  let range = RANGES.some(([k]) => k === lsGet("pk95mon-range")) ? lsGet("pk95mon-range") : "24h";
  const groups = groupBy(series, (x) => x.gk);
  box.innerHTML = `<div class="chart-bar"><div class="seg" role="group" aria-label="Zeitraum">${RANGES.map(([k, l]) => `<button type="button" data-range="${k}" aria-pressed="${k === range}">${l}</button>`).join("")}</div>
      <span class="muted small" data-ch-info></span></div>
    <details class="panel sp" data-sp><summary><span class="sp-title">${icon("list")} Werte auswählen</span><span class="muted small" data-sp-count></span>${icon("chevron-down", "sp-chev")}</summary>
      <div class="sp-groups">${groups.map(([k, list]) => `<div class="sp-group"><b>${esc(names[k] || "Weitere")}</b>${list.map((x) => `<label class="check"><input type="checkbox" data-sid="${esc(x.uid)}" ${sel.has(x.uid) ? "checked" : ""}><span>${esc(metricLabel(x.name))}${x.instance ? ` <span class="muted">${esc(x.instance)}</span>` : ""}</span></label>`).join("")}</div>`).join("")}</div>
      <div class="flex mt"><button class="btn gray sm" type="button" data-sp-def>Standardauswahl</button><button class="btn gray sm" type="button" data-sp-none>Keine</button></div></details>
    <div class="charts" data-charts></div>`;
  const charts = qs("[data-charts]", box);
  let cache = null, loadTimer = null, loadSeq = 0;
  const count = () => { qs("[data-sp-count]", box).textContent = `${sel.size} von ${series.length}`; };
  const save = () => { lsSet(selKey, JSON.stringify(series.filter((x) => sel.has(x.uid)).map(skey))); };
  const drawCharts = () => {
    if (!cache || !alive()) return;
    const by = groupBy(cache.series || [], (x) => x.name + "|" + (x.unit || metricUnitOf(x.name)));
    const di = (n) => { const i = DEFAULT_SERIES.indexOf(n); return i < 0 ? 99 : i; };
    by.sort(([a], [b]) => di(a.split("|")[0]) - di(b.split("|")[0]) || metricLabel(a.split("|")[0]).localeCompare(metricLabel(b.split("|")[0]), "de"));
    const from = cache.from || nowSec() - RANGE_SECS[range], to = cache.to || nowSec();
    qs("[data-ch-info]", box).textContent = `${fmtTime(from, true)} bis ${fmtTime(to, true)}${cache.step ? ` · ein Punkt je ${fmtUptime(cache.step)}` : ""}`;
    if (!by.length) { charts.innerHTML = empty("Keine Daten im Zeitraum", "", "", "chart"); return; }
    const lbl = (x) => x.label || x.instance || metricLabel(x.name);
    charts.innerHTML = by.map(([k, list], gi) => {
      const [n, unit] = k.split("|");
      list.sort((a, b) => (a.label !== undefined) - (b.label !== undefined) || String(lbl(a)).localeCompare(String(lbl(b)), "de", { numeric: true }));
      const last = (x) => { for (let i = (x.points || []).length - 1; i >= 0; i--) if (isNum(x.points[i][1])) return x.points[i][1]; return null; };
      const legend = list.length > 1 || list[0].instance || list[0].label ? `<div class="legend">${list.map((x, i) => `<span><i class="sw s${i % N_COLORS}"></i>${esc(lbl(x))} <b>${esc(fmtVal(last(x), unit))}</b></span>`).join("")}</div>` : `<div class="legend"><span>zuletzt <b>${esc(fmtVal(last(list[0]), unit))}</b></span></div>`;
      return `<div class="panel chart-panel"><div class="panel-head"><div class="panel-title">${esc(metricLabel(n))}</div><span class="muted small mono">${esc(n)}</span></div>${legend}
        <div class="chart-box" tabindex="0" role="img" aria-label="${esc(`Diagramm ${metricLabel(n)}, ${list.length} ${list.length === 1 ? "Reihe" : "Reihen"} – mit den Pfeiltasten durch die Werte gehen`)}" data-chart="${gi}"></div></div>`;
    }).join("");
    qsa("[data-chart]", charts).forEach((cb) => { const [k, list] = by[+cb.dataset.chart]; drawChart(cb, list, k.split("|")[1], from, to, cache.step); });
  };
  const load = async () => {
    const chosen = series.filter((x) => sel.has(x.uid));
    if (!chosen.length) { cache = null; charts.innerHTML = empty("Keine Werte ausgewählt", "Oben unter „Werte auswählen“ ankreuzen, was du sehen möchtest.", "", "chart"); return; }
    charts.classList.add("loading");
    const seq = ++loadSeq;
    try {
      // The API answers at most MAX_CHART_SERIES series of one device per request; larger selections are split.
      const reqs = [];
      for (const [dev, list] of groupBy(chosen, (x) => x.dev)) {
        for (let i = 0; i < list.length; i += MAX_CHART_SERIES) reqs.push([dev, list.slice(i, i + MAX_CHART_SERIES)]);
      }
      const parts = await Promise.all(reqs.map(([dev, list]) => GET(`/api/devices/${enc(dev)}/metrics?series=${list.map((x) => enc(x.id)).join(",")}&range=${range}`)
        .then((r) => ({ ...r, series: (r.series || []).map((m) => { const x = byUid.get(`${dev}:${m.id}`); return x && x.prefix ? { ...m, label: `${x.prefix} · ${m.instance || metricLabel(m.name)}` } : m; }) }))));
      if (!alive() || seq !== loadSeq) return; // a newer load (range or selection change) wins
      const own = parts.find((p, i) => reqs[i][0] === dv.id) || parts[0];
      cache = { ...own, step: Math.max(...parts.map((p) => p.step || 0)) || own.step, series: parts.flatMap((p) => p.series || []) };
      drawCharts();
    } finally { if (seq === loadSeq) charts.classList.remove("loading"); }
  };
  qsa("[data-range]", box).forEach((b) => b.onclick = () => {
    range = b.dataset.range; lsSet("pk95mon-range", range);
    qsa("[data-range]", box).forEach((x) => x.setAttribute("aria-pressed", x === b));
    load().catch(toastErr);
  });
  const changed = () => { count(); save(); clearTimeout(loadTimer); loadTimer = setTimeout(() => load().catch(toastErr), 350); };
  qsa("[data-sid]", box).forEach((x) => x.onchange = () => { if (x.checked) sel.add(x.dataset.sid); else sel.delete(x.dataset.sid); changed(); });
  const setAll = (n) => { sel = n; qsa("[data-sid]", box).forEach((x) => { x.checked = sel.has(x.dataset.sid); }); changed(); };
  qs("[data-sp-def]", box).onclick = () => { lsSet(selKey, ""); setAll(defaultSelection(series)); };
  qs("[data-sp-none]", box).onclick = () => setAll(new Set());
  count();
  onResize = () => { if (alive()) drawCharts(); };
  await load();
}

// ---------- check page (/devices/<id>/checks/<key>): one check with its charts ----------
// CHART_PAIRS: metrics drawn together in one chart [names, title, labels, second line dashed].
const CHART_PAIRS = [
  [["net.rx", "net.tx"], "Datenverkehr", ["Empfangen", "Gesendet"]],
  [["if.rx", "if.tx"], "Datenverkehr", ["Empfangen", "Gesendet"]],
  [["guest.netin", "guest.netout"], "Netzwerk", ["Empfangen", "Gesendet"]],
  [["guest.diskread", "guest.diskwrite"], "Datenträger", ["Lesen", "Schreiben"]],
  [["ups.input", "ups.output"], "Spannung", ["Eingang", "Ausgang"]],
  [["mem.used", "mem.total"], "Arbeitsspeicher belegt", ["Belegt", "Gesamt"], true],
  [["disk.used", "disk.total"], "Belegt", ["Belegt", "Gesamt"], true],
  [["storage.used", "storage.total"], "Belegt", ["Belegt", "Gesamt"], true],
  [["poe.power", "poe.budget"], "PoE-Leistung", ["Verbrauch", "Budget"], true],
];
const seriesUnit = (s) => s.unit || metricUnitOf(s.name);
// checkCharts groups the series of one check into charts: the pairs above, else one chart per
// metric name (several instances as several lines); ordered like the check's metrics.
function checkCharts(series, c) {
  const order = ((c && c.metrics) || []).map((m) => m.n);
  const rank = (n) => (n === "uptime" ? 9999 : order.includes(n) ? order.indexOf(n) : 100 + (DEFAULT_SERIES.includes(n) ? DEFAULT_SERIES.indexOf(n) : 900));
  const multi = new Set(series.map((s) => s.instance || "")).size > 1;
  const used = new Set(), out = [];
  for (const [names, title, labels, dash] of CHART_PAIRS) {
    const list = series.filter((s) => names.includes(s.name));
    if (!list.some((s) => s.name === names[0])) continue;
    list.forEach((s) => used.add(s));
    list.sort((a, b) => names.indexOf(a.name) - names.indexOf(b.name) || String(a.instance).localeCompare(String(b.instance), "de", { numeric: true }));
    out.push({ title, names, unit: seriesUnit(list[0]), rank: Math.min(...names.map(rank)),
      list: list.map((s) => ({ ...s, label: labels[names.indexOf(s.name)] + (multi && s.instance ? " " + s.instance : ""), dash: !!dash && names.indexOf(s.name) === 1 })) });
  }
  for (const [n, list] of groupBy(series.filter((s) => !used.has(s)), (s) => s.name)) {
    list.sort((a, b) => String(a.instance).localeCompare(String(b.instance), "de", { numeric: true }));
    out.push({ title: metricLabel(n), names: [n], unit: seriesUnit(list[0]), rank: rank(n), list: list.map((s) => ({ ...s, label: list.length > 1 && s.instance ? s.instance : metricLabel(n) })) });
  }
  return out.sort((a, b) => a.rank - b.rank || a.title.localeCompare(b.title, "de"));
}
// effThreshold returns the threshold that applies to a metric (see DESIGN.md "Evaluation"): device
// with instance > device > global with instance > global > default; with its source.
function effThreshold(dev, global, defs, n, inst) {
  const pick = (list) => (inst ? (list || []).find((t) => t.metric === `${n}:${inst}`) : null) || (list || []).find((t) => t.metric === n);
  for (const [list, src] of [[dev, "Dieses Gerät"], [global, "Einstellungen"], [defs, "Standard"]]) { const t = pick(list); if (t) return { t, src }; }
  return null;
}

PAGES["/devices/:id/checks/:key"] = async (el, token) => {
  const m = location.pathname.match(/^\/devices\/([^/]+)\/checks\/([^/]+)$/);
  const dec = (s) => { try { return decodeURIComponent(s); } catch (e) { return s; } };
  const id = dec(m[1]), key = dec(m[2]);
  let data;
  try { data = await GET(`/api/devices/${enc(id)}`); }
  catch (e) {
    if (e.status !== 404) throw e;
    el.innerHTML = pageHead("Gerät nicht gefunden", "", "", ["/devices", "Geräte"]) + empty("Dieses Gerät gibt es nicht (mehr)", "", `<a class="btn" href="/devices" data-go>Zu den Geräten</a>`, "server");
    return;
  }
  if (token !== pageToken) return;
  const alive = () => token === pageToken;
  const rk = "pk95mon-check-range:" + (state.user || "");
  let range = RANGES.some(([k]) => k === lsGet(rk)) ? lsGet(rk) : "24h";
  let series = null, cache = null, seq = 0, settings, alertName = "", busy = 0;
  const view = () => projectStatus(data.status || {}, data.device.rules);
  // check: {c, hidden} of the current status; c is null when the device does not report the check
  const check = () => {
    const st = view(), c = (st.checks || []).find((x) => x.key === key);
    if (c) return { c, hidden: false };
    const h = (st.hidden || []).find((x) => x.key === key);
    return h ? { c: null, hidden: true, name: h.name, group: h.group, pending: h.pending } : { c: null, hidden: false };
  };
  el.innerHTML = `<div data-head></div>
    <div class="chart-bar ckd-bar"><h2 class="ckd-h">Verlauf</h2><div class="seg" role="group" aria-label="Zeitraum">${RANGES.map(([k, l]) => `<button type="button" data-range="${k}" aria-pressed="${k === range}">${l}</button>`).join("")}</div>
      <span class="muted small" data-ch-info></span><a class="ckd-all small" href="/devices/${enc(id)}#charts" data-go>Alle Diagramme des Geräts</a></div>
    <div class="charts" data-charts>${loadingPanel()}</div>
    <div class="ckd-cols"><section class="panel" data-facts aria-label="Werte und Details"></section><section class="panel" data-thr aria-label="Schwellwerte"></section></div>
    <div class="chart-bar ckd-bar"><h2 class="ckd-h">Alarmverlauf</h2><span class="muted small">Wann dieser Check auffällig war</span></div>
    <div data-hist>${loadingPanel()}</div>`;
  const head = qs("[data-head]", el), charts = qs("[data-charts]", el), info = qs("[data-ch-info]", el);
  const factsBox = qs("[data-facts]", el), thrBox = qs("[data-thr]", el), hist = qs("[data-hist]", el);

  const reload = async () => {
    const r = await GET(`/api/devices/${enc(id)}`);
    if (!alive()) return;
    data = r; state.ovAt = 0;
    drawHead(); drawFacts(); drawThr();
  };
  // saveRules changes the device's rules: fresh GET, mutate, PUT (as on the device page)
  const saveRules = (btn, fn) => run(btn, async () => {
    busy++;
    try {
      const cur = (await GET(`/api/devices/${enc(id)}`)).device;
      cur.rules = fn(cur.rules || []);
      const r = await PUT(`/api/devices/${enc(id)}`, cur);
      data.device.rules = r && Array.isArray(r.rules) ? r.rules : cur.rules;
      await reload();
    } finally { busy--; }
  }, "Gespeichert.");

  const drawHead = () => {
    const dv = data.device, par = data.parent || null, { c, hidden, name: hName, group: hGroup, pending } = check();
    const name = (c && c.name) || hName || alertName || key;
    const back = par ? [`/devices/${enc(par.id)}#agent-${enc(dv.id)}`, `${par.name} / ${attLabel(dv)}`] : [`/devices/${enc(dv.id)}`, dv.name];
    const group = (c && c.group) || hGroup || "";
    const sub = [par ? esc(dv.name) : "", group ? esc(group) : "", c && c.since ? `Zustand ${sinceCell(c)}` : "", `<span class="mono">${esc(key)}</span>`].filter(Boolean).join(" · ");
    const mode = ruleMode(dv.rules || [], key);
    const modeB = c && key !== REACH_KEY ? `<button class="btn gray ck-mode${isQuiet(c) ? " quiet" : ""}" type="button" data-ck-mode data-fk="mode" aria-haspopup="menu" aria-expanded="false" aria-label="Überwachung: ${esc(RULE_MODE_SHORT[mode])} – ändern">${icon(mode === "quiet" ? "eye" : "bell")} ${esc(RULE_MODE_SHORT[mode])}</button>` : "";
    const pillH = c ? ckPill(c) : hidden ? pill("Ausgeblendet") : pill("Nicht vorhanden");
    let top = "";
    if (c) {
      const v = cardValue(c), tone = cardTone(c), msg = reasonText(c);
      const val = v.rxtx ? `<span class="svc-rx"><span aria-hidden="true">↓</span> ${esc(fmtVal(v.rxtx[0], "B/s"))}<span class="sr-only"> empfangen</span></span><span class="svc-tx"><span aria-hidden="true">↑</span> ${esc(fmtVal(v.rxtx[1], "B/s"))}<span class="sr-only"> gesendet</span></span>` : esc(v.text);
      top = `<div class="panel ckd-now st-${tone}"><div class="ckd-main"><div class="svc-val${v.rxtx ? " rxtx" : v.word ? " word" : ""}">${val}</div>${v.sub ? `<div class="svc-sec">${esc(v.sub)}</div>` : ""}</div>
        ${msg || (isQuiet(c) && MUTED_TITLE[c.muted]) || c.pending ? `<div class="ckd-msg">${reasonHTML(c)}${pendingTag(c)}${c.reason && c.msg && c.reason !== c.msg ? `<div class="t-sub">${esc(c.msg)}</div>` : ""}</div>` : ""}
        ${isQuiet(c) ? `<div class="t-sub">Nur Anzeige: Werte werden aufgezeichnet, der Check löst keinen Alarm aus.</div>` : ""}</div>`;
    } else if (hidden) {
      top = callout("", "Ausgeblendet", pending ? "Der Check erscheint mit der nächsten Abfrage wieder." : "Dieser Check wird durch eine Regel verworfen – es werden keine neuen Werte gespeichert. Der bisherige Verlauf steht unten.",
        pending ? "" : `<button class="btn gray sm" type="button" data-unhide data-fk="unhide">${icon("eye")} Wieder anzeigen</button>`);
    } else {
      top = callout("yellow", "Diesen Check gibt es zurzeit nicht", "Das Gerät meldet ihn nicht (mehr) – zum Beispiel ein gelöschter Gast oder ein entfernter Dienst. Der bisherige Verlauf und die Alarme stehen unten.");
    }
    const ae = document.activeElement, fk = ae && head.contains(ae) ? ae.getAttribute("data-fk") : null;
    head.innerHTML = pageHead(`${esc(name)} ${pillH}`, sub,
      `${modeB}<button class="btn gray" type="button" data-poll data-fk="poll" ${dv.enabled === false ? "disabled" : ""}>${icon("refresh")} Jetzt prüfen</button>`, back) + top;
    if (fk) { const n = qs(`[data-fk="${CSS.escape(fk)}"]`, head); if (n) n.focus({ preventScroll: true }); }
    qs("[data-poll]", head).onclick = (e) => run(e.currentTarget, async () => {
      const r = await POST(`/api/devices/${enc(id)}/poll`);
      if (!alive()) return;
      if (r.status) data.status = r.status;
      state.ovAt = 0;
      drawHead(); drawFacts();
      toast("Geprüft.");
    });
    const mb = qs("[data-ck-mode]", head);
    if (mb) mb.onclick = () => {
      const rules = data.device.rules || [], r = ruleFor(rules, key);
      const hint = !r ? "" : r.match === key ? "Eigene Einstellung für diesen Check." : `Zurzeit durch die Regel <code>${esc(r.match)}</code>.`;
      modeMenu(mb, { title: name, current: ruleMode(rules, key), hint, onPick: (mo) => saveRules(mb, (rs) => setExactRule(rs, key, mo)) });
    };
    const ub = qs("[data-unhide]", head);
    if (ub) ub.onclick = () => saveRules(ub, (rs) => setExactRule(rs, key, "alert"));
  };

  const drawFacts = () => {
    const { c } = check();
    const ms = (c && c.metrics) || [], facts = c && c.facts ? Object.entries(c.facts) : [];
    const rows = [
      ...ms.map((x) => [instLabel(x.n, x.i), fmtVal(x.v, mUnit(x))]),
      ...facts,
      ...(c && c.msg ? [["Meldung", c.msg]] : []),
      ...(c && c.since ? [["Zustand seit", fmtTime(c.since, true)]] : []),
      ["Letzte Prüfung", data.status && data.status.last_poll ? `${fmtTime(data.status.last_poll, true)} (${fmtAgo(data.status.last_poll)})` : "—"],
    ];
    factsBox.innerHTML = `<div class="panel-head"><div class="panel-title">Werte und Details</div></div>
      <table class="kv ckd-kv">${rows.map(([k, v]) => `<tr><td>${esc(k)}</td><td>${esc(v)}</td></tr>`).join("")}</table>`;
  };

  const drawThr = () => {
    const { c } = check();
    const link = `<a class="btn gray sm" href="/devices/${enc(id)}#settings" data-go>${icon("pencil")} Schwellwerte ändern</a>`;
    const headH = `<div class="panel-head"><div class="panel-title">Schwellwerte</div>${link}</div>`;
    if (settings === undefined) { thrBox.innerHTML = headH + `<div class="flex muted small"><span class="spinner"></span> Lade …</div>`; return; }
    if (settings === null) { thrBox.innerHTML = headH + `<p class="muted small">Die Schwellwerte konnten nicht geladen werden.</p>`; return; }
    const seen = new Set(), rows = [];
    for (const x of (c && c.metrics) || []) {
      const k = x.n + "|" + (x.i || "");
      if (seen.has(k)) continue;
      seen.add(k);
      const e = effThreshold(data.device.thresholds, settings.thresholds, settings.defaults, x.n, x.i || "");
      if (e) rows.push([instLabel(x.n, x.i), e.t.disabled ? "aus" : thrText(e.t), e.src]);
    }
    thrBox.innerHTML = headH + (rows.length ? `<div class="table-wrap"><table class="ckd-thr"><thead><tr><th>Messwert</th><th>Grenze</th><th>Gilt aus</th></tr></thead><tbody>
        ${rows.map(([n, t, s]) => `<tr><td>${esc(n)}</td><td>${esc(t)}</td><td class="muted small nowrap">${esc(s)}</td></tr>`).join("")}</tbody></table></div>`
      : `<p class="muted small">${c && (c.metrics || []).length ? "Für die Werte dieses Checks gilt keine Grenze – der Zustand kommt vom Gerät selbst." : "Dieser Check hat keine Messwerte; der Zustand kommt vom Gerät selbst (z. B. Dienst läuft / gestoppt)."}</p>`) +
      (c && isQuiet(c) ? `<p class="t-sub" style="margin-top:8px">Nur Anzeige: Grenzen werden ausgewertet, lösen aber keinen Alarm aus.</p>` : "");
  };

  // series of this check: by check_key; series stored before the key was known by name+instance
  const pickSeries = (all) => {
    const { c } = check();
    const mk = new Set(((c && c.metrics) || []).map((x) => x.n + "|" + (x.i || "")));
    return all.filter((s) => s.check_key === key || (!s.check_key && mk.has(s.name + "|" + (s.instance || ""))));
  };
  const drawCharts = () => {
    if (!cache || !alive()) return;
    const { c } = check();
    const defs = checkCharts(series, c);
    const { from, to, step } = cache;
    info.textContent = `${fmtTime(from, true)} bis ${fmtTime(to, true)}${step ? ` · ein Punkt je ${fmtUptime(step)}` : ""}`;
    const last = (x) => { for (let i = (x.points || []).length - 1; i >= 0; i--) if (isNum(x.points[i][1])) return x.points[i][1]; return null; };
    defs.forEach((d) => { d.full = d.list.map((s) => ({ ...s, points: (cache.byId.get(s.id) || {}).points || [] })); });
    charts.innerHTML = defs.map((d, i) => {
      const legend = d.full.length > 1 ? `<div class="legend">${d.full.map((x, j) => `<span><i class="sw ${chartCls(d.full, j)}${x.dash ? " dash" : ""}"></i>${esc(x.label)} <b>${esc(fmtVal(last(x), d.unit))}</b></span>`).join("")}</div>`
        : `<div class="legend"><span>zuletzt <b>${esc(fmtVal(last(d.full[0]), d.unit))}</b></span></div>`;
      return `<div class="panel chart-panel"><div class="panel-head"><div class="panel-title">${esc(d.title)}</div><span class="muted small mono">${esc(d.names.join(" · "))}</span></div>${legend}
        <div class="chart-box" tabindex="0" role="img" aria-label="${esc(`Diagramm ${d.title}, ${d.full.length} ${d.full.length === 1 ? "Reihe" : "Reihen"} – mit den Pfeiltasten durch die Werte gehen`)}" data-chart="${i}"></div></div>`;
    }).join("");
    qsa("[data-chart]", charts).forEach((cb) => { const d = defs[+cb.dataset.chart]; drawChart(cb, d.full, d.unit, from, to, step); });
  };
  const loadCharts = async () => {
    const my = ++seq;
    try {
      const first = series === null;
      series = pickSeries(await deviceSeries(id));
      if (!series.length && first) series = pickSeries(await deviceSeries(id, 0));
      if (!alive() || my !== seq) return;
      if (!series.length) {
        cache = null; info.textContent = "";
        const { c } = check();
        charts.innerHTML = empty("Noch keine Messwerte", c && (c.metrics || []).length ? "Die ersten Werte erscheinen nach der nächsten Abfrage." : "Dieser Check meldet nur einen Zustand, keine Messwerte. Wann er auffällig war, zeigt der Alarmverlauf.", "", "chart");
        return;
      }
      charts.classList.add("loading");
      const reqs = [];
      for (let i = 0; i < series.length; i += MAX_CHART_SERIES) reqs.push(series.slice(i, i + MAX_CHART_SERIES));
      const parts = await Promise.all(reqs.map((l) => GET(`/api/devices/${enc(id)}/metrics?series=${l.map((x) => enc(x.id)).join(",")}&range=${enc(range)}`)));
      if (!alive() || my !== seq) return;
      const p0 = parts[0] || {};
      cache = { from: p0.from || nowSec() - RANGE_SECS[range], to: p0.to || nowSec(), step: Math.max(0, ...parts.map((p) => p.step || 0)), byId: new Map(parts.flatMap((p) => p.series || []).map((s) => [s.id, s])) };
      drawCharts();
    } catch (e) {
      if (alive() && my === seq && !cache) charts.innerHTML = callout("red", "Die Messwerte konnten nicht geladen werden", esc(e.message));
      else if (alive()) toastErr(e);
    } finally { if (my === seq) charts.classList.remove("loading"); }
  };
  const loadHist = async () => {
    const r = await GET(`/api/alerts?device=${enc(id)}&state=all&limit=500`);
    if (!alive()) return;
    const list = (r.alerts || []).filter((a) => a.check_key === key);
    const nm = list.length ? list[0].check_name : "";
    hist.innerHTML = list.length ? alertsTable(list, { device: false, check: false }) + (r.more ? `<p class="muted small" style="margin:0 12px">Nur die letzten 500 Alarme des Geräts sind berücksichtigt – ältere unter <a href="/alerts?tab=history" data-go>Alarme</a>.</p>` : "")
      : empty("Noch keine Alarme", "Dieser Check war bisher nicht auffällig.", "", "bell");
    bindAck(hist, loadHist);
    if (nm && !alertName) { alertName = nm; if (!check().c) drawHead(); }
  };

  qsa("[data-range]", el).forEach((b) => b.onclick = () => {
    range = b.dataset.range; lsSet(rk, range);
    qsa("[data-range]", el).forEach((x) => x.setAttribute("aria-pressed", x === b));
    loadCharts();
  });
  drawHead(); drawFacts(); drawThr();
  GET("/api/settings").then((r) => { settings = r; }, () => { settings = null; }).then(() => { if (alive()) drawThr(); });
  loadHist().catch((e) => { if (alive()) hist.innerHTML = callout("red", "Die Alarme konnten nicht geladen werden", esc(e.message)); });
  onResize = () => { if (alive()) drawCharts(); };
  await loadCharts();
  every(60000, token, async () => {
    if (modeMenuOpen || busy) return;
    const r = await GET(`/api/devices/${enc(id)}`);
    if (!alive()) return;
    data = r; drawHead(); drawFacts(); drawThr();
    if (["1h", "6h", "24h"].includes(range)) await loadCharts();
    await loadHist();
  });
};

// ---------- alerts ----------
PAGES["/alerts"] = async (el, token) => {
  let tab = new URLSearchParams(location.search).get("tab") === "history" ? "all" : "active";
  let reload = null;
  const alive = () => token === pageToken;
  el.innerHTML = pageHead("Alarme", "Ein Alarm entsteht, wenn ein Check auf Warnung oder Kritisch wechselt, und endet, wenn er wieder OK ist. Quittieren heißt: jemand kümmert sich – erneute Benachrichtigungen bleiben dann aus.",
    `<div class="seg" role="group" aria-label="Ansicht"><button type="button" data-t="active" aria-pressed="${tab === "active"}">Aktiv</button><button type="button" data-t="all" aria-pressed="${tab === "all"}">Verlauf</button></div>
     <button class="btn gray" type="button" data-refresh>${icon("refresh")} Aktualisieren</button>`) + `<div data-list>${loadingPanel()}</div>`;
  const box = qs("[data-list]", el);
  const load = async () => {
    reload = await alertsBlock(box, `state=${tab}`, tab === "active"
      ? { device: true, emptyTitle: "Keine aktiven Alarme", emptyText: "Alles im grünen Bereich." }
      : { device: true, emptyTitle: "Noch keine Alarme", emptyText: "Hier steht später, wann welcher Check auffällig war." }, alive);
  };
  qsa("[data-t]", el).forEach((b) => b.onclick = () => {
    tab = b.dataset.t;
    qsa("[data-t]", el).forEach((x) => x.setAttribute("aria-pressed", x === b));
    history.replaceState({}, "", "/alerts" + (tab === "all" ? "?tab=history" : ""));
    box.innerHTML = loadingPanel();
    load().catch(toastErr);
  });
  qs("[data-refresh]", el).onclick = (e) => run(e.currentTarget, () => (reload ? reload(false) : load()));
  await load();
  every(30000, token, () => (tab === "active" && reload ? reload(false) : null));
};

// ---------- events ----------
PAGES["/events"] = async (el, token) => {
  let sev = "", list = [], more = false;
  const load = async (append) => {
    const before = append && list.length ? list[list.length - 1].id : 0;
    const r = await GET(`/api/events?severity=${enc(sev)}&before=${enc(before)}`);
    if (token !== pageToken) return;
    const got = r.events || [];
    list = append ? list.concat(got) : got;
    more = r.more !== undefined ? !!r.more : got.length >= 50;
    const open = new Set(qsa("details.ev-details[open]", el).map((x) => x.dataset.id));
    qs("[data-list]", el).innerHTML = list.length ? `<div class="panel flush"><div class="rows">${list.map((e) => eventRow(e, false)).join("")}</div></div>${more ? `<div style="text-align:center;margin:12px"><button class="btn gray" type="button" data-more>Ältere laden</button></div>` : ""}` : empty("Keine Ereignisse", "", "", "bell");
    qsa("details.ev-details", el).forEach((x) => { const id = x.closest(".rows") ? list[qsa(".rows > *", el).indexOf(x)]?.id : ""; x.dataset.id = id; if (open.has(String(id))) x.open = true; });
    const m = qs("[data-more]", el);
    if (m) m.onclick = () => run(m, () => load(true));
  };
  el.innerHTML = pageHead("Ereignisse", "Was der Server beobachtet hat: Alarme, „Wieder OK“, neue Agents, Verbindungsprobleme und Systemmeldungen. Benachrichtigungen dafür stellst du unter Einstellungen ein.",
    `<select data-sev aria-label="Schweregrad" style="width:auto"><option value="">Alle Stufen</option><option value="warning">ab Warnung</option><option value="critical">nur Kritisch</option></select>`) + `<div data-list>${loadingPanel()}</div>`;
  qs("[data-sev]", el).onchange = (e) => { sev = e.target.value; load(false); };
  await load(false);
  every(30000, token, () => (list.length <= 50 ? load(false) : null));
};

// ---------- Agents ----------
function cmpVer(a, b) {
  const pa = String(a || "").replace(/^v/, "").split(/[.+-]/).map((x) => parseInt(x, 10) || 0);
  const pb = String(b || "").replace(/^v/, "").split(/[.+-]/).map((x) => parseInt(x, 10) || 0);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) { const d = (pa[i] || 0) - (pb[i] || 0); if (d) return d; }
  return 0;
}
const isVer = (v) => /^v?\d+(\.\d+)*/.test(String(v || ""));

function agentList(list, version) {
  if (!list.length) return `<div style="padding:0 16px 16px">${empty("Noch kein Agent angemeldet", "Führe einen der Befehle oben auf einem Server aus – nach etwa einer Minute steht er hier.", "", "desktop")}</div>`;
  return `<div class="table-wrap"><table class="resp"><thead><tr><th>Zustand</th><th>Server</th><th>Gehört zu</th><th>Betriebssystem</th><th>Agent</th><th>Letzter Kontakt</th></tr></thead><tbody>
    ${[...list].sort(byName).map((d) => {
      const st = devState(d);
      const old = isVer(d.agent_version) && isVer(version) && cmpVer(d.agent_version, version) < 0;
      return `<tr class="${st === "disabled" ? "off" : ""}">
        <td data-l="Zustand">${statePill(st)}</td>
        <td data-l="Server"><a class="t-main" href="/devices/${enc(d.id)}" data-go>${esc(d.name)}</a><div class="t-sub mono">${esc(d.host || "")}</div></td>
        <td data-l="Gehört zu" class="small">${d.parent_id ? `<a href="/devices/${enc(d.parent_id)}" data-go>${esc(d.parent_name || "Gerät")}</a>` : '<span class="muted">—</span>'}</td>
        <td data-l="Betriebssystem" class="small">${esc(d.platform || "—")}<div class="t-sub">${esc(OS_LABELS[d.os] || d.os || "")}</div></td>
        <td data-l="Agent">${d.agent_version ? (old ? pill(`${d.agent_version} – veraltet`, "amber") : `<span class="mono small">${esc(d.agent_version)}</span>`) : '<span class="muted">—</span>'}</td>
        <td data-l="Letzter Kontakt" class="small nowrap" title="${esc(fmtTime(d.last_poll, true))}">${esc(fmtAgo(d.last_poll))}</td></tr>`;
    }).join("")}</tbody></table></div>`;
}

PAGES["/agents"] = async (el, token) => {
  const draw = async () => {
    const r = await GET("/api/agents");
    if (token !== pageToken) return;
    const bins = r.binaries || [];
    el.innerHTML = pageHead("Agents", "Der PK95-Agent läuft als Dienst auf Linux- und Windows-Servern und schickt seine Werte per HTTPS an diesen Server – auch hinter einer Firewall, ohne offene Ports auf dem Server.") +
      `<div class="panel"><div class="panel-head"><div class="panel-title">So kommt ein Server dazu</div></div><ol class="steps">
        <li><span><b>Befehl kopieren</b> – unten für Linux (Debian, Ubuntu, Proxmox-Hosts …) oder Windows (Server 2016 und neuer, Windows 10/11).</span></li>
        <li><span><b>Auf dem Server ausführen</b> – als root bzw. in einer PowerShell „Als Administrator“. Das Skript lädt den Agent von hier, prüft die SHA-256-Prüfsumme, meldet ihn mit dem Anmeldeschlüssel an und richtet den Dienst ein.</span></li>
        <li><span><b>Fertig</b> – nach etwa einer Minute steht der Server unten in der Liste. Dienste und Schwellwerte stellst du auf seiner Seite unter „Einstellungen“ ein.</span></li></ol></div>
      <div class="grid">
        <div class="panel"><div class="panel-head"><div class="panel-title">Linux</div>${pill("bash", "")}</div>
          <p class="panel-intro">Als root in der Shell des Servers:</p>${r.linux_cmd ? codeBlock(r.linux_cmd) : callout("yellow", "Nicht verfügbar", "Es fehlt die Agent-Datei für Linux.")}</div>
        <div class="panel"><div class="panel-head"><div class="panel-title">Windows</div>${pill("PowerShell", "")}</div>
          <p class="panel-intro">In einer PowerShell, die „Als Administrator“ läuft:</p>${r.windows_cmd ? codeBlock(r.windows_cmd) : callout("yellow", "Nicht verfügbar", "Es fehlt die Agent-Datei für Windows.")}</div>
      </div>
      <div class="grid">
        <div class="panel"><div class="panel-head"><div class="panel-title">Anmeldung</div><div class="actions"><button class="btn gray sm" type="button" data-rotate>${icon("key")} Neu erzeugen</button></div></div>
          <table class="kv">
            <tr><td>Anmeldeschlüssel</td><td><div class="code">${`<pre>${esc(r.enroll_key || "—")}</pre>`}<button class="ib" type="button" title="Kopieren" aria-label="Anmeldeschlüssel kopieren" data-copy="${esc(r.enroll_key || "")}">${icon("copy")}</button></div><div class="t-sub">Nur für die Installation. Jeder Agent bekommt danach eigene Zugangsdaten.</div></td></tr>
            <tr><td>Server-Adresse</td><td class="mono">${esc(r.server_url || "—")}<div class="t-sub" style="font-family:var(--font)">So erreichen die Agents diesen Server. Ändern unter <a href="/settings" data-go>Einstellungen → Allgemein</a>.</div></td></tr>
            <tr><td>Fingerabdruck</td><td><span class="mono small">${esc(r.fingerprint || "—")}</span><div class="t-sub">Der Agent prüft das Zertifikat dieses Servers anhand des Fingerabdrucks – auch wenn es selbst signiert ist.</div></td></tr>
            <tr><td>Version</td><td>${esc(r.version || "—")}</td></tr>
            <tr><td>Updates</td><td>${r.auto_update ? `${pill("automatisch", "green")} <span class="small muted">Agents aktualisieren sich selbst auf diese Version.</span>` : `${pill("aus")} <span class="small muted">Einschalten unter <a href="/settings" data-go>Einstellungen → Agents</a>.</span>`}</td></tr>
          </table></div>
        <div class="panel flush"><div class="panel-head"><div class="panel-title">Agent-Dateien</div></div>
          ${bins.length ? `<div class="table-wrap"><table class="resp"><thead><tr><th>System</th><th>Datei</th><th class="num">Größe</th><th>SHA-256</th><th></th></tr></thead><tbody>
          ${bins.map((b) => `<tr class="${b.available ? "" : "off"}"><td data-l="System"><b>${esc(OS_LABELS[b.os] || b.os)}</b><div class="t-sub">${esc(ARCH_LABELS[b.arch] || b.arch)}</div></td>
            <td data-l="Datei" class="mono small">${esc(b.file)}</td>
            <td data-l="Größe" class="num small">${b.available ? esc(fmtBytes(b.size)) : "—"}</td>
            <td data-l="SHA-256">${b.sha256 ? `<span class="mono small" title="${esc(b.sha256)}">${esc(b.sha256.slice(0, 12))}…</span> <button class="ib gray sm-ib" type="button" title="Prüfsumme kopieren" aria-label="Prüfsumme kopieren" data-copy="${esc(b.sha256)}">${icon("copy")}</button>` : '<span class="muted">—</span>'}</td>
            <td class="act">${b.available ? `<a class="btn gray sm" href="/agent/download/${enc(b.os)}/${enc(b.arch)}" download>${icon("download")} Laden</a>` : pill("fehlt", "rose")}</td></tr>`).join("")}
          </tbody></table></div>` : `<div style="padding:0 16px 16px">${empty("Keine Agent-Dateien", "Im Docker-Image liegen sie unter /opt/pk95monitoring/agents.", "", "download")}</div>`}
          <p class="muted small" style="padding:12px 16px 16px;margin:0">Manuell: <code>pk95agent install --server URL --key SCHLÜSSEL --fingerprint SHA256</code>. Entfernen: <code>pk95agent uninstall</code>.
            Für einen Proxmox-Knoten oder ein anderes Gerät, das der Agent ergänzen soll: auf dessen Seite „Agent hinzufügen“ (oder <code>--device ID --node KNOTEN</code>).</p></div>
      </div>
      <div class="panel flush"><div class="panel-head"><div class="panel-title">Angemeldete Agents <span class="muted small">${(r.agents || []).length}</span></div></div><div data-agents>${agentList(r.agents || [], r.version)}</div></div>`;
    bindCopy(el);
    qs("[data-rotate]", el).onclick = async (e) => {
      const btn = e.currentTarget;
      if (await confirmDialog({ title: "Neuen Anmeldeschlüssel erzeugen?", text: "Der bisherige Schlüssel funktioniert danach nicht mehr für neue Installationen, auch nicht in schon kopierten Befehlen. Angemeldete Agents bleiben verbunden – sie haben eigene Zugangsdaten.", button: "Neu erzeugen", tone: "", ic: "key" }))
        run(btn, async () => { await POST("/api/agents/key"); await draw(); }, "Neuer Anmeldeschlüssel erzeugt.");
    };
  };
  el.innerHTML = pageHead("Agents", "Lade …");
  await draw();
  every(30000, token, async () => {
    const r = await GET("/api/agents");
    if (token !== pageToken) return;
    const box = qs("[data-agents]", el);
    if (box) box.innerHTML = agentList(r.agents || [], r.version);
  });
};

// ---------- settings ----------
// formSubmit binds a section form: values → PUT/POST, errors at the fields (section prefix stripped).
function formSubmit(form, fn, prefix) {
  form.onsubmit = async (e) => {
    e.preventDefault();
    const btn = qs('button[type="submit"]', form);
    if (btn) btn.disabled = true;
    try { showErrors(form, null); await fn(formValues(form)); }
    catch (err) { showErrors(form, prefix ? stripErr(err, prefix) : err); }
    finally { if (btn) btn.disabled = false; }
  };
}

PAGES["/settings"] = async (el, token) => {
  const [sys, set, tok] = await Promise.all([GET("/api/system"), GET("/api/settings"), GET("/api/tokens").catch(() => ({ tokens: [] }))]);
  if (token !== pageToken) return;
  const g = set.general || {}, n = set.notify || {}, cen = set.central || {}, agc = set.agent || {};
  const has = (k) => !!(n["has_" + k] || set["has_" + k]);
  const origin = location.origin;
  el.innerHTML = pageHead("Einstellungen", "Allgemeines, Benachrichtigungen, Schwellwerte und die Anbindung an eine Zentrale.") + `<div class="grid">
    <div>
      <div class="panel"><div class="panel-head"><div class="panel-title">Allgemein</div></div>
        <form class="form" data-general novalidate><div class="form-error" hidden></div><div class="form-grid">
          ${field({ name: "instance", label: "Instanzname / Kunde", value: g.instance, placeholder: "z. B. Muster GmbH", hint: "Steht oben in der Leiste, in Benachrichtigungen und bei der Zentrale." })}
          ${field({ name: "public_url", label: "Öffentliche Adresse für Agents", value: g.public_url, mono: true, placeholder: origin, hint: "So erreichen Agents diesen Server, z. B. https://monitoring.kunde.de:8443. Leer = Adresse aus dem Browser." })}
          ${field({ name: "retention_raw_days", label: "Rohdaten aufbewahren (Tage)", type: "number", min: 1, value: g.retention_raw_days, hint: "Jeder einzelne Messwert." })}
          ${field({ name: "retention_5m_days", label: "5-Minuten-Werte (Tage)", type: "number", min: 1, value: g.retention_5m_days })}
          ${field({ name: "retention_1h_days", label: "Stundenwerte (Tage)", type: "number", min: 1, value: g.retention_1h_days, hint: "Für die Ansicht „1 Jahr“: mindestens 366." })}
          ${field({ name: "renotify_hours", label: "Erneut benachrichtigen nach (Std.)", type: "number", min: 0, value: g.renotify_hours ?? 0, hint: "Solange ein Alarm offen und nicht quittiert ist. 0 = nie." })}
        </div><div><button class="btn" type="submit">Speichern</button></div></form></div>

      <div class="panel"><div class="panel-head"><div class="panel-title">Benachrichtigungen</div></div>
        <form class="form" data-notify novalidate><div class="form-error" hidden></div>
          <div class="form-grid">
            ${field({ name: "min_severity", label: "Melden ab", type: "select", value: n.min_severity || "warning", options: [["info", "Alles – auch „Wieder OK“ und Systemmeldungen"], ["warning", "Warnungen und kritische Alarme"], ["critical", "Nur kritische Alarme"]], wide: true })}
          </div>
          <div class="section"><div class="form-grid">
            ${field({ name: "mail_enabled", label: "Per E-Mail", type: "checkbox", value: n.mail_enabled, wide: true })}
            ${field({ name: "smtp_host", label: "SMTP-Server", value: n.smtp_host, mono: true, placeholder: "smtp.example.org" })}
            ${field({ name: "smtp_port", label: "Port", type: "number", value: n.smtp_port || 587 })}
            ${field({ name: "smtp_security", label: "Verschlüsselung", type: "select", value: n.smtp_security || "starttls", options: [["starttls", "STARTTLS (587)"], ["tls", "TLS (465)"], ["none", "keine"]] })}
            ${field({ name: "smtp_user", label: "Benutzer", value: n.smtp_user })}
            ${field({ name: "smtp_password", label: "Passwort", type: "password", placeholder: has("smtp_password") ? "unverändert" : "" })}
            ${field({ name: "mail_from", label: "Absender", value: n.mail_from, placeholder: "monitoring@example.org" })}
            ${field({ name: "mail_to", label: "Empfänger", value: n.mail_to, placeholder: "ich@example.org, …", wide: true })}
            ${field({ name: "mail_links", label: "Knopf „Im Monitoring ansehen“ in Mails", type: "checkbox", value: n.mail_links, wide: true, hint: "Nur einschalten, wenn die Empfänger sich hier anmelden können." })}
          </div></div>
          <div class="section"><div class="form-grid">
            ${field({ name: "hook_enabled", label: "Per Push (ntfy, Gotify, Webhook)", type: "checkbox", value: n.hook_enabled, wide: true })}
            ${field({ name: "hook_kind", label: "Art", type: "select", value: n.hook_kind || "ntfy", options: [["ntfy", "ntfy"], ["gotify", "Gotify"], ["json", "Webhook (JSON)"]] })}
            ${field({ name: "hook_url", label: "URL", value: n.hook_url, mono: true, placeholder: "https://ntfy.sh/mein-thema", wide: true })}
            ${field({ name: "hook_token", label: "Token (optional)", type: "password", placeholder: has("hook_token") ? "unverändert" : "" })}
          </div></div>
          <div class="section"><div class="form-grid">
            ${field({ name: "telegram_enabled", label: "Per Telegram", type: "checkbox", value: n.telegram_enabled, wide: true, hint: "Bot bei @BotFather anlegen (/newbot), dem Bot eine Nachricht schreiben oder ihn in die Gruppe aufnehmen – dann „Chat-ID ermitteln“." })}
            ${field({ name: "telegram_token", label: "Bot-Token", type: "password", mono: true, placeholder: has("telegram_token") ? "unverändert" : "123456789:AA…", wide: true })}
            ${field({ name: "telegram_chat", label: "Chat-ID", value: n.telegram_chat, mono: true, placeholder: "z. B. 123456789 oder -100…", button: "Chat-ID ermitteln", wide: true, hint: "Person, Gruppe oder Kanal (@kanalname). Gruppen-IDs beginnen mit einem Minus." })}
            ${field({ type: "html", name: "telegram_found", wide: true, html: `<div data-tg-chats></div>` })}
          </div></div>
          <div class="flex"><button class="btn" type="submit">Speichern</button><button class="btn gray" type="button" data-test>Testnachricht senden</button></div>
        </form></div>
    </div>
    <div>
      <div class="panel"><div class="panel-head"><div class="panel-title">System</div></div>
        <table class="kv">
          <tr><td>Version</td><td>PK95 Monitoring ${esc(sys.version)} ${sys.demo ? pill("Demo", "amber") : ""}</td></tr>
          <tr><td>Instanz</td><td data-sys-instance>${esc(sys.instance || "—")}</td></tr>
          <tr><td>Läuft seit</td><td>${sys.started ? `${esc(fmtTime(sys.started, true))} <span class="muted">(${esc(fmtUptime(nowSec() - sys.started))})</span>` : "—"}</td></tr>
          <tr><td>Geräte</td><td>${esc(fmtNum(sys.devices, 0))}</td></tr>
          <tr><td>Messreihen</td><td>${esc(fmtNum(sys.series, 0))}</td></tr>
          <tr><td>Datenbank</td><td>${esc(fmtBytes(sys.db_size))}</td></tr>
          <tr><td>Datenverzeichnis</td><td class="mono">${esc(sys.data_dir || "—")}</td></tr>
          <tr><td>Angemeldet</td><td>${esc(state.user)}</td></tr></table></div>

      <div class="panel"><div class="panel-head"><div class="panel-title">Agents</div></div>
        <form class="form" data-agentset novalidate><div class="form-error" hidden></div>
          ${field({ name: "auto_update", label: "Agents automatisch aktualisieren", type: "checkbox", value: agc.auto_update, hint: "Ältere Agents laden beim nächsten Bericht die Version dieses Servers, prüfen die Prüfsumme und starten neu." })}
          <div class="flex"><button class="btn" type="submit">Speichern</button><a class="btn gray" href="/agents" data-go>Zu den Agents</a></div></form></div>

      <div class="panel"><div class="panel-head"><div class="panel-title">Passwort ändern</div></div>
        <form class="form" data-pw novalidate><div class="form-error" hidden></div><div class="form-grid">
          ${field({ name: "old", label: "Aktuelles Passwort", type: "password", autocomplete: "current-password" })}
          ${field({ name: "new", label: "Neues Passwort", type: "password", hint: "Mindestens 10 Zeichen.", autocomplete: "new-password" })}
        </div><div><button class="btn" type="submit">Passwort ändern</button></div></form></div>
    </div></div>

    <div class="panel"><div class="panel-head"><div class="panel-title">Schwellwerte</div><div class="actions"><button class="btn gray sm" type="button" data-th-reset>${icon("restore")} Auf Standard zurücksetzen</button></div></div>
      <p class="panel-intro">Gelten für alle Geräte, solange ein Gerät keine eigenen hat. Ohne Eintrag gelten die eingebauten Standardwerte (unten). „Unterhalb“: schlecht, wenn der Wert kleiner ist. „Anzahl“: so viele Werte in Folge, bevor der Zustand wechselt.</p>
      <form class="form" data-thr novalidate><div class="form-error" hidden></div><div data-th></div>
        <div class="flex"><button class="btn" type="submit">Speichern</button></div></form>
      <details class="howto mt"><summary>Eingebaute Standardwerte</summary>
        <div class="table-wrap"><table class="resp"><thead><tr><th>Messwert</th><th>Grenzen</th></tr></thead><tbody>
          ${(set.defaults || []).map((t) => `<tr><td data-l="Messwert">${esc(metricLabel(splitMetric(t.metric)[0]))} <span class="muted mono small">${esc(t.metric)}</span></td><td data-l="Grenzen" class="small">${esc(thrText(t))}</td></tr>`).join("")}
        </tbody></table></div></details></div>

    <div class="grid">
      <div class="panel flush"><div class="panel-head"><div class="panel-title">API-Token</div><div class="actions"><button class="btn" type="button" data-tok-add>${icon("plus")} Token erstellen</button></div></div>
        <p class="panel-intro">Für eine Zentrale oder andere Programme: nur lesender Zugriff auf Zusammenfassung, Geräte und Alarme.</p>
        <div data-tokens></div>
        <div style="padding:0 16px 16px"><div class="section-title" style="margin-bottom:6px">Beispiel</div>${codeBlock(`curl -s -H "Authorization: Bearer <TOKEN>" ${origin}/api/v1/summary`)}
          <p class="muted small mt">Weitere Endpunkte: <code>/api/v1/devices</code>, <code>/api/v1/devices/&lt;id&gt;</code>, <code>/api/v1/alerts?state=active</code>. Bei selbst signiertem Zertifikat curl mit <code>-k</code> oder <code>--cacert</code> aufrufen.</p></div></div>
      <div class="panel"><div class="panel-head"><div class="panel-title">Zentrale</div></div>
        <p class="panel-intro">Optional: Die Zusammenfassung regelmäßig an eine zentrale Instanz schicken – praktisch, wenn diese den Server nicht direkt erreicht.</p>
        <form class="form" data-central novalidate><div class="form-error" hidden></div><div class="form-grid">
          ${field({ name: "enabled", label: "Zusammenfassung an die Zentrale senden", type: "checkbox", value: cen.enabled, wide: true })}
          ${field({ name: "url", label: "URL der Zentrale", value: cen.url, mono: true, wide: true, placeholder: "https://zentrale.example.org/api/push" })}
          ${field({ name: "token", label: "Token", type: "password", mono: true, placeholder: cen.has_token ? "unverändert" : "", hint: "Wird als Bearer-Token mitgeschickt." })}
          ${field({ name: "interval", label: "Alle (Minuten)", type: "number", min: 1, value: cen.interval || 5 })}
        </div><div><button class="btn" type="submit">Speichern</button></div></form></div>
    </div>`;
  bindCopy(el);

  // Allgemein
  formSubmit(qs("[data-general]", el), async (v) => {
    const r = await PUT("/api/settings", { general: { instance: v.instance.trim(), public_url: v.public_url.trim(), retention_raw_days: v.retention_raw_days, retention_5m_days: v.retention_5m_days, retention_1h_days: v.retention_1h_days, renotify_hours: v.renotify_hours } });
    const inst = (r.general || {}).instance ?? v.instance.trim();
    setInstance(inst);
    qs("[data-sys-instance]", el).textContent = inst || "—";
    toast("Gespeichert.");
  }, "general");

  // Benachrichtigungen
  const nf = qs("[data-notify]", el);
  const nv = () => { const v = formValues(nf); delete v.tg_pick; v.smtp_port = Number(v.smtp_port) || 587; return { notify: v }; };
  nf.onsubmit = async (e) => { e.preventDefault(); try { showErrors(nf, null); await PUT("/api/settings", nv()); toast("Gespeichert."); } catch (err) { showErrors(nf, stripErr(err, "notify")); } };
  const tgBtn = qs('[data-fbtn="telegram_chat"]', el), tgBox = qs("[data-tg-chats]", el);
  tgBtn.onclick = () => run(tgBtn, async () => {
    showErrors(nf, null);
    tgBox.innerHTML = "";
    let r;
    try { r = await POST("/api/settings/telegram/chats", { token: qs('[name="telegram_token"]', nf).value }); }
    catch (err) { showErrors(nf, err); return; }
    const chats = r.chats || [];
    const set1 = (c) => { qs('[name="telegram_chat"]', nf).value = String(c.id); qs('[name="telegram_enabled"]', nf).checked = true; tgBox.innerHTML = ""; toast(`Chat „${c.title || c.id}“ übernommen – jetzt speichern.`); };
    if (!chats.length) {
      tgBox.innerHTML = callout("yellow", "Noch keine Nachricht an den Bot", "Schreib dem Bot in Telegram etwas (z. B. /start). Für eine Gruppe: Bot hinzufügen und dort eine Nachricht schreiben. Dann noch einmal „Chat-ID ermitteln“.");
      return;
    }
    if (chats.length === 1) { set1(chats[0]); return; }
    const kind = { private: "Person", group: "Gruppe", supergroup: "Gruppe", channel: "Kanal" };
    tgBox.innerHTML = `<div class="fl">Welcher Chat?</div><div class="radio-cards">${chats.map((c, i) => `<label class="radio-card"><input type="radio" name="tg_pick" value="${i}"><span><b>${esc(c.title || String(c.id))}</b><small>${esc(kind[c.type] || c.type)} · <span class="mono">${esc(c.id)}</span></small></span></label>`).join("")}</div>`;
    qsa('[name="tg_pick"]', tgBox).forEach((x) => x.onchange = () => set1(chats[+x.value]));
  });
  qs("[data-test]", el).onclick = (e) => run(e.currentTarget, async () => {
    showErrors(nf, null);
    try { await POST("/api/settings/test", nv()); toast("Testnachricht verschickt."); }
    catch (err) { showErrors(nf, stripErr(err, "notify")); }
  });

  // Schwellwerte
  const defs = set.defaults || [];
  const thr = thresholdEditor(qs("[data-th]", el), set.thresholds || [], (m) => findThr(defs, m), "Standard");
  formSubmit(qs("[data-thr]", el), async () => {
    const r = await PUT("/api/settings", { thresholds: thr.read() });
    if (r && r.thresholds) thr.set(r.thresholds);
    toast("Schwellwerte gespeichert.");
  });
  qs("[data-th-reset]", el).onclick = async (e) => {
    const btn = e.currentTarget;
    if (await confirmDialog({ title: "Schwellwerte zurücksetzen?", text: "Alle globalen Schwellwerte werden entfernt; danach gelten die eingebauten Standardwerte. Eigene Schwellwerte einzelner Geräte bleiben erhalten.", button: "Zurücksetzen", ic: "restore" }))
      run(btn, async () => { await PUT("/api/settings", { thresholds: [] }); thr.set([]); }, "Auf Standard zurückgesetzt.");
  };

  // API-Token
  const drawTokens = (list) => {
    qs("[data-tokens]", el).innerHTML = list.length ? `<div class="table-wrap"><table class="resp"><thead><tr><th>Name</th><th>Token</th><th>Erstellt</th><th>Zuletzt benutzt</th><th></th></tr></thead><tbody>
      ${list.map((t) => `<tr><td data-l="Name" class="t-main">${esc(t.name)}</td><td data-l="Token" class="mono small">${esc(t.prefix || "")}…</td>
        <td data-l="Erstellt" class="small nowrap">${esc(fmtTime(t.created, true))}</td><td data-l="Zuletzt benutzt" class="small nowrap">${t.last_used ? esc(fmtAgo(t.last_used)) : '<span class="muted">nie</span>'}</td>
        <td class="act"><button class="ib red" type="button" title="Löschen" aria-label="Token ${esc(t.name)} löschen" data-tok-del="${esc(t.id)}">${icon("trash")}</button></td></tr>`).join("")}
      </tbody></table></div>` : `<div style="padding:0 16px 16px" class="muted small">Noch kein API-Token.</div>`;
    qsa("[data-tok-del]", el).forEach((b) => b.onclick = async () => {
      const t = list.find((x) => String(x.id) === b.dataset.tokDel);
      if (await confirmDialog({ title: `Token „${t ? t.name : ""}“ löschen?`, text: "Programme, die ihn benutzen, bekommen danach keinen Zugriff mehr.", button: "Löschen" }))
        run(b, async () => { await DEL(`/api/tokens/${enc(b.dataset.tokDel)}`); drawTokens((await GET("/api/tokens")).tokens || []); }, "Token gelöscht.");
    });
  };
  drawTokens(tok.tokens || []);
  qs("[data-tok-add]", el).onclick = () => formDialog({
    title: "API-Token erstellen", ic: "key", button: "Erstellen", cls: "",
    text: "Der Token erlaubt nur lesenden Zugriff auf die externe API (/api/v1/…).",
    fields: [{ name: "name", label: "Name", required: true, placeholder: "z. B. Zentrale", wide: true }],
    submit: async (v) => {
      const r = await POST("/api/tokens", { name: v.name.trim() });
      drawTokens((await GET("/api/tokens")).tokens || []);
      setTimeout(() => {
        const d = openDialog(`<div class="dlg-head"><span class="dlg-ic">${icon("key")}</span><div class="grow"><div class="dlg-title">API-Token „${esc(r.name || v.name)}“</div>
            <p class="dlg-text">Kopiere ihn jetzt – er wird nur dieses eine Mal angezeigt und lässt sich nicht wieder abrufen.</p></div></div>
          <div class="dlg-body">${codeBlock(r.token || "")}${callout("yellow", "Wie ein Passwort behandeln", "Wer den Token hat, sieht alle Geräte, Zustände und Alarme dieser Instanz.")}</div>
          <div class="dlg-foot"><button class="btn" type="button" data-close>Fertig</button></div>`);
        bindCopy(d);
        qs("[data-close]", d).onclick = () => d.close();
        qs("[data-copy]", d).focus();
      }, 0);
    },
  });

  // Zentrale
  formSubmit(qs("[data-central]", el), async (v) => {
    await PUT("/api/settings", { central: { enabled: v.enabled, url: v.url.trim(), token: v.token, interval: v.interval || 5 } });
    qs('[name="token"]', el).value = "";
    toast("Gespeichert.");
  }, "central");

  // Agents
  formSubmit(qs("[data-agentset]", el), async (v) => { await PUT("/api/settings", { agent: { auto_update: v.auto_update } }); toast("Gespeichert."); }, "agent");

  // Konto
  const pf = qs("[data-pw]", el);
  formSubmit(pf, async (v) => { await POST("/api/account/password", { old: v.old, new: v.new }); pf.reset(); toast("Passwort geändert. Andere Sitzungen wurden abgemeldet."); });
};

render();
