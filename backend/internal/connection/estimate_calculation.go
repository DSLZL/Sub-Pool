package connection

import ()

func quotaUsed(snapshot QuotaSnapshot, kind string) *string {
	for _, w := range snapshot.Windows {
		if w.Kind == kind {
			return w.UsedPercent
		}
	}
	return nil
}
