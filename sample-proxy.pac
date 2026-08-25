function FindProxyForURL(url, host) {
    if (isPlainHostName(host)) {
        return "DIRECT";
    }

    if (shExpMatch(host, "*.local")) {
        return "DIRECT";
    }

    var resolvedIp = dnsResolve(host);
    if (isInNet(resolvedIp, "10.0.0.0", "255.0.0.0") ||
        isInNet(resolvedIp, "172.16.0.0", "255.240.0.0") ||
        isInNet(resolvedIp, "192.168.0.0", "255.255.0.0") ||
        isInNet(resolvedIp, "127.0.0.0", "255.0.0.0")) {
        return "DIRECT";
    }

    return "PROXY localhost:1357; DIRECT";
}
