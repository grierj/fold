package main

import (
	"strconv"
	"time"

	"fold/internal/jobs"
)

// Job identity that SQL or a second file also spells. Each job type owns its
// attempt count (jobs.Type); the payload keys that raw SQL reads and the
// unique-key shapes live here once, and job_identity_test.go checks them
// against the payload structs and the one trigger that queues a job.

// Announcements (messages_send.go): a send rides out a short blip, a batch
// rides out a provider outage of about an hour (30 seconds doubling).
var (
	messageSendType  = jobs.Type{Name: kindMessageSend, MaxAttempts: 5}
	messageBatchType = jobs.Type{Name: kindMessageBatch, MaxAttempts: 8}
)

// messageJobIDField is the JSON key of messageSendJob and messageBatchJob
// that the reaper reads in SQL.
const messageJobIDField = "messageId"

// messageSendKey carries the time, so rescheduling queues a new job and the
// old one finds the message not yet due and does nothing.
func messageSendKey(id uint, when time.Time) string {
	return kindMessageSend + ":" + idString(id) + ":" + strconv.FormatInt(when.Unix(), 10)
}

// messageBatchKeyPrefix starts the keys of a message's batches. gen is "" for
// the first send and a fresh value for each "Try again", so a retry's keys
// never collide with the jobs already done.
func messageBatchKeyPrefix(messageID uint, gen string) string {
	prefix := kindMessageBatch + ":" + idString(messageID) + ":"
	if gen != "" {
		prefix += gen + ":"
	}
	return prefix
}

// Push (push_deliver.go): one push.deliver job per device.
var (
	pushDeliverType = jobs.Type{Name: kindPushDeliver}
	// pushDevicesOffType is queued only by the users trigger, which leaves
	// max_attempts at its column default (the test pins that it equals this).
	pushDevicesOffType = jobs.Type{Name: kindPushDevicesOff}
)

// pushJobRecipientField is pushJob's JSON key for an announcement or
// reminder row, which the reaper reads in SQL.
const pushJobRecipientField = "messageRecipientId"

func pushNotificationKey(notificationID, deviceID uint) string {
	return "push:n:" + idString(notificationID) + ":d:" + idString(deviceID)
}

// pushRecipientKey queues one announcement or reminder row for one device,
// once, however often it runs.
func pushRecipientKey(recipientID, deviceID uint) string {
	return "push:mr:" + idString(recipientID) + ":d:" + idString(deviceID)
}

// avatarCleanupUserField is avatarCleanupJob's JSON key, which the sweep
// reads in SQL.
const avatarCleanupUserField = "userId"
