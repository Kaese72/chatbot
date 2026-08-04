# Chatbot

This repository contains the chatbot, aimed to be hosted on the *appliance* and have 
access to the services on the *appliance* it hosted on. You supply an API key (currently Anthropic)
via the **API key** API and the chatbot is available in the UI to have conversations with.

Some basic requirements

* The bot should be locked down to only have access to services within the cluster (no external access for not)
* The bot is authenticated as its own *user*, to the system, and makes actions via that user. 
* The bot is built as a (optionally multi replica) golang service which takes conversation requests from the user and forwards them to an LLM together with the available tools
* The bot takes care of a conversation behind the scenes

## API

The api for this bot is a REST like API focusing on conversations, which contains a chronological
dialog. You start a conversation with a query, the chatbot does what it does, during which another 
query can not be added to *that* conversation. However, the user should be able to terminate
any such actions at any time.

Conversations are global, meaning that all users can see all conversations. No privacy here.

### Conversation

The **conversations** entity simply contains the top level information about a conversation, like

* Status
  * USER_INPUT
  * AGENT_IN_PROGRESS
* Currently processed by what replica
* name (currently first 255 characters of first query)

And similar information.
The main purpose of this object is to list and navigate between conversations

A Conversation is the host for a *dialog*, which is just a list of back and forths. but these 
are separate models in the API.

Most user interactions happen via the **Conversation** APIs, like

* `/chatbot-service/v0/conversations`
  * GET a list of conversations
* `/chatbot-service/v0/conversations/new`
  * POST a new conversation with an initial query
* `/chatbot-service/v0/conversations/{\d+}`
* `/chatbot-service/v0/conversations/{\d+}/input`
  * POST input to an existing conversation. The user must have the initiative
* `/chatbot-service/v0/conversations/{\d+}/terminate`
  * POST a termination to the conversation, preventing further interactions by the agent 
    currently handling this conversation, returning initiative to the user
* `/chatbot-service/v0/conversations/{\d+}/forget`
  * Completely forgets a conversation where the user currently has initiative
* `/chatbot-service/v0/conversations/{\d+}/follow/{\d+}`
  * Opens a SSE stream which feeds **DialogEntries** to the user
  * The second ID is the ID of a **DialogEntry** which the stream should follow (after)

### DialogEntry

A **DialogEntry** is just a single message or piece of information in a **Conversation** during 
the back and forth. A **DialogEntry** can represent communication from the user or from the agent,
and which one it is is determined by a *type* field. This field determiens what attributes
are available in the API response. This field also decides how the information should be rendered
in the UI. 

The **DialogEntry** keeps track of what **Conversation** it belongs to, and in what order it is. 
The order is maintained by a global ID (Primary key) that is always incremented when new
**DialogEntries** are added. There may be "gaps" in the IDs from the perspective of a **Conversation**
but this is fine as long as the order is maintained.

DialogEntries are mostly read-only, and input to an existing conversation happens on **Converation**
endpoints.

The **DialogEntry** types can be one of the following

* USER_INPUT
* USER_STOP
* AGENT_MESSAGE
* AGENT_ERROR
* AGENT_GENERIC_TOOL_CALL
* AGENT_GENERIC_TOOL_RESPONSE
* AGENT_DEVICE_CAPABILITY_TRIGGER_CALL
* AGENT_DEVICE_CAPABILITY_TRIGGER_RESPONSE
* AGENT_GROUP_CAPABILITY_TRIGGER_CALL
* AGENT_GROUP_CAPABILITY_TRIGGER_RESPONSE

Currently A LOT goes into the AGENT_GENERIC_TOOL_CALL, where we simply record the name of the
tool and the input+output as text blobs. For all other *types*, we expect all information
to be strictly structured.

All of these have some things in common, the "base". The base consists of attributes we expect
all **DialogEntries** to contain. Those are

* ID (primary key, globally incremented)
* Created (time submitted to the database)
* ConversationId
* Type (the distinguisher)
* Initiative
  * AGENT, or
  * USER

### API Key

The **API key** entity holds the credential(s) used to talk to an LLM provider. It has

* name (a human-readable label)
* type (currently only ANTHROPIC)
* active (a boolean; at most one API key is active at a time)
* value (the secret key itself)

There is no verification that a key is actually valid (has credits, isn't revoked, etc.) when
it is added -- that is only discovered the next time it is actually used.

Endpoints:

* `/chatbot-service/v0/api-keys`
  * GET a list of API keys (the secret value is never included in responses)
  * POST a new API key
* `/chatbot-service/v0/api-keys/{\d+}`
  * GET a single API key (the secret value is never included)
  * PATCH an existing API key (name, active, and/or value)
  * DELETE an API key

Marking an API key active deactivates whichever key was previously active, atomically -- there
is always at most one active key. Any endpoint that would result in new traffic to the LLM
(starting a new conversation, submitting input to one) fetches the active key from the database
at the moment it is invoked; if none is active, that endpoint fails with HTTP/503 and a
presentable error message rather than proceeding.

## Architecture

### Replica owns active dialog of conversation

During a conversation, the conversation is locked while the agent figures out what it 
needs to do and does it, before the initiative is turned back to the user. During the
duration of a lock, a single replica of the deployment handles the communication with
the LLM. This can take quite a while and is subject to unforeseen termination.

For this reason there needs to be some guards for the sake of integrity. These guards
are in the form of locks and exclusivity. The following steps are all happening within
a single replica.

1. Service receives input from user
2. Aqcuire the *lock* on the conversation if possible
   1. If no, return HTTP/409 to user
   2. If yes, proceed
3. Save the user input in the database
   1. USER_INPUT **DialogEntry** saved
   2. Set *initiative* to AGENT on **Conversation**
4. Send RabbitMQ message, indicating there is an update the **Conversation**
5. Send user input to the LLM
6. For every message back from the LLM, do
   1. Re-acquire the *lock* in the **Conversation**
   2. Read entire content block from LLM SSE stream
   3. Save the content block in the database
   4. Send RabbitMQ message, indicating there is an update the **Conversation**
   5. Do this until the LLM have finished giving us messages (stop reason)
7. If the response contained *tool calls*,
   1. For each tool call
      1. Resolve the tool call, saving the result in the database
      2. Send RabbitMQ message, indicating there is an update the **Conversation**
   2. Send all resolved tool calls back to the LLM and go back to processing messages (#6)
8. If the response contained another error, create **DialogEntry** stating what the error is
9. Set the *initiative* to USER
10. Release the *lock* on the conversation
11. Send RabbitMQ message, indicating there is an update the **Conversation**

When processing these kinds of things, before taking action (like executing tool calls
or continueing the conversation with the LLM), it needs to check whether it has been 
terminated via a RabbitMQ event sent from a user requesting to terminate. The 
event targets the entire **Conversation**.

### Replica **Conversation** lock

Whenever a **Conversation** start being processed, the thread/goroutine generates
an ID for itself, and records that ID on the **Conversation** entity in the database
together with a timestamp of when that lock was aqcuired. As long as that thread/goroutine
is still processing that **Conversation** by handling *tool calls* or saving LLM messages,
the lock timestamp should be updated (re-acquisition). 

If the lock timestamp has drifted more than 300 seconds, another thread/goroutine 
will no longer consider the **Conversation** locked, and may take it over if it receives requests
that requires processing of that **Conversation**. This does not guarantee that the previous
thread/goroutine is dead, however, the previous process should stop processing whatever
it is processing when it realizes it no longer has the lock, which it should do every
time it tries to save something to the database, which it should do before returning 
anything to the user. 

While a replica handles messages from the LLM, for every message recieved, thread/goroutine
must start a new transaction for each message, re-acquire the lock, do the update, and commit
the transaction. Then it will process the next message from the LLM in the stream. 

When the *initiative* is handed back to the user, the *lock* needs to be released.

When the *initiative* is with the USER, the *lock* must not be acquirable. When the user
sends input, the *initative* is changed to AGENT first, then the lock is acquired within 
that transaction.

When aqcuiring the lock, the lock must not actively (dictated by the lock timeout) acquired
by anything, including themselves. When re-acquiring, the lock must already be locked by 
its own ID. Ie. With SQL like the following,

* For acquiring the lock
  * SQL:
    ```sql
    UPDATE conversations
    SET lock_id = :new_lock_id,
        locked_at = NOW()
    WHERE id = :conversation_id
      AND (lock_id IS NULL OR locked_at < NOW() - INTERVAL 300 SECOND);
    ```
    * Only succeeds if the lock is not taken, or expired
    * The caller must check the affected row count; 0 rows means someone else holds
      an unexpired lock, and the request should fail with HTTP/409
* For re-acquiring the lock
  * SQL:
    ```sql
    UPDATE conversations
    SET locked_at = NOW()
    WHERE id = :conversation_id
      AND lock_id = :own_lock_id;
    ```
    * Fails if the lock is cleared or taken by someone else
    * Deliberately does not re-check the 300 second expiry window here: if
      `lock_id` still equals `:own_lock_id`, nobody has stolen the lock yet
      (stealing is the only way `lock_id` changes), so the expiry check is
      redundant. The caller must still check the affected row count; 0 rows
      means the lock was lost and processing must stop before returning
      anything to the user

### Terminating agent deliberation

When a request to terminate a conversation is received, we send a RabbitMQ message
which every single replica picks up, and then terminates the appropriate 
conversation thread/goroutine by sending a message on a channel it keeps track of, 
adding a **DialogEntry** for the termination once it is 
successful (read by the thing handling the LLM interactions).

### Service holds tools

The service that receives the API call also handles the tool resolution and deals with it
internally.

### Configuration

The service itself needs to have one thing configured at deploy time:

* JWT for user access (This will change later, but for now its fine)

The Anthropic API key is *not* deploy-time config -- it is stored in the database and managed
at runtime via the **API key** API described above.