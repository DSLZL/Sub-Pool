const localPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?$/;
const utcPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/;

function wallClock(timeZone: string) {
  const format = new Intl.DateTimeFormat("en-CA", {
    timeZone, calendar: "iso8601", numberingSystem: "latn",
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  });
  return (instant: number) => {
    const { year, month, day, hour, minute, second } = Object.fromEntries(format.formatToParts(instant).map(part => [part.type, part.value]));
    return year!.padStart(4, "0") + "-" + month + "-" + day + "T" + hour + ":" + minute + ":" + second;
  };
}

export function pickerDateTime(value: string, timezone: string) {
  if (!value || localPattern.test(value)) return value;
  if (!utcPattern.test(value)) return "";
  const instant = Date.parse(value);
  return Number.isFinite(instant) && new Date(instant).toISOString().slice(0, 19) === value.slice(0, 19) ? wallClock(timezone)(instant) : "";
}

export function pickerTimeToUTC(value: string, timezone: string) {
  if (!localPattern.test(value)) return null;
  const local = value.length === 16 ? value + ":00" : value;
  const at = Date.parse(local + "Z");
  if (!Number.isFinite(at) || new Date(at).toISOString().slice(0, 19) !== local) return null;

  const format = wallClock(timezone);
  const offsets = new Set<number>();
  const hour = 3600000;
  // shortcut: hourly IANA sampling follows usageCalendar; use explicit transitions if more than one change can occur in an hour.
  for (let sample = at - 24 * hour; sample <= at + 24 * hour; sample += hour)
    offsets.add(Date.parse(format(sample) + "Z") - sample);
  const matches = [...offsets].map(offset => at - offset).filter(instant => format(instant) === local);
  return matches.length === 1 ? new Date(matches[0]!).toISOString() : null;
}
