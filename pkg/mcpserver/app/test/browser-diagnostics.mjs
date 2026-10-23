// Keep browser diagnostics in memory as counts/booleans; never retain payload text.
export function diagnosticCollector(secrets) {
  const pending = new Set();
  let consoleCount = 0, pageErrorCount = 0, privateLeak = false, scanFailed = false;
  const inspect = value => {
    const text = typeof value === 'string' ? value : JSON.stringify(value);
    if (secrets.some(secret => text?.includes(secret)) || /[a-f0-9]{64}/.test(text ?? '')) privateLeak = true;
  };
  return {
    console(message) {
      consoleCount++;
      const scan = Promise.resolve().then(async () => {
        inspect(message.text());
        for (const arg of message.args()) inspect(await arg.jsonValue());
      }).catch(() => { scanFailed = true; }).finally(() => pending.delete(scan));
      pending.add(scan);
    },
    pageError() { pageErrorCount++; },
    async drain() {
      while (pending.size) await Promise.all([...pending]);
      return { consoleCount, pageErrorCount, privateLeak, scanFailed };
    }
  };
}
