#define _POSIX_C_SOURCE 200809L

#include "native.h"

#include <eos_sdk.h>
#include <eos_connect.h>
#include <eos_lobby.h>
#include <eos_p2p.h>

#include <mutex>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#ifdef _WIN32
#include <windows.h>
#define AJB_SLEEP_MS(ms) Sleep(ms)
#else
#include <unistd.h>
#define AJB_SLEEP_MS(ms) usleep((ms) * 1000)
#endif

#define AJB_SOCKET_NAME "AJB_RELAY_V1"
#define AJB_P2P_CHANNEL 1
#define AJB_MAX_PACKET_SIZE 1170
#define AJB_MAX_ID_LENGTH 128
#define AJB_OPERATION_TIMEOUT_MS 60000

struct ajb_eos {
    EOS_HPlatform platform;
    EOS_ProductUserId local_user;
    char lobby_id[AJB_MAX_ID_LENGTH];
    EOS_NotificationId request_notification;
    EOS_NotificationId member_status_notification;
    std::mutex api_mutex;
    int initialized;
    int owns_lobby;
};

typedef struct ajb_callback_state {
    int done;
    EOS_EResult result;
    EOS_ProductUserId product_user;
    EOS_ContinuanceToken continuance_token;
    char string_result[AJB_MAX_ID_LENGTH];
} ajb_callback_state;

static void ajb_leave_lobby(ajb_eos* eos);
static void EOS_CALL ajb_connection_request_callback(const EOS_P2P_OnIncomingConnectionRequestInfo* data);
static void EOS_CALL ajb_lobby_member_status_callback(const EOS_Lobby_LobbyMemberStatusReceivedCallbackInfo* data);
static int ajb_product_user_to_string(EOS_ProductUserId user, char* output, uint32_t output_size);

static void ajb_log_peer_event(const char* event, EOS_ProductUserId user) {
    char user_id[EOS_PRODUCTUSERID_MAX_LENGTH + 1] = {};
    if (user == NULL || !ajb_product_user_to_string(user, user_id, sizeof(user_id))) {
        snprintf(user_id, sizeof(user_id), "%s", "unknown");
    }
    fprintf(stderr, "[EOS] %s: %s\n", event, user_id);
    fflush(stderr);
}

static void ajb_set_error(char* error, uint32_t error_size, const char* message) {
    if (error != NULL && error_size > 0) {
        snprintf(error, error_size, "%s", message != NULL ? message : "unknown EOS error");
    }
}

static void ajb_set_result_error(char* error, uint32_t error_size, const char* operation, EOS_EResult result) {
    const char* result_name = EOS_EResult_ToString(result);
    if (error != NULL && error_size > 0) {
        snprintf(error, error_size, "%s failed: %s", operation, result_name != NULL ? result_name : "unknown EOS result");
    }
}

static void EOS_CALL ajb_device_id_callback(const EOS_Connect_CreateDeviceIdCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    state->done = 1;
}

static void EOS_CALL ajb_create_user_callback(const EOS_Connect_CreateUserCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    state->product_user = data->LocalUserId;
    state->done = 1;
}

static void EOS_CALL ajb_connect_login_callback(const EOS_Connect_LoginCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    state->product_user = data->LocalUserId;
    state->continuance_token = data->ContinuanceToken;
    state->done = 1;
}

static void EOS_CALL ajb_create_lobby_callback(const EOS_Lobby_CreateLobbyCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    snprintf(state->string_result, sizeof(state->string_result), "%s", data->LobbyId != NULL ? data->LobbyId : "");
    state->done = 1;
}

static void EOS_CALL ajb_join_lobby_callback(const EOS_Lobby_JoinLobbyByIdCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    snprintf(state->string_result, sizeof(state->string_result), "%s", data->LobbyId != NULL ? data->LobbyId : "");
    state->done = 1;
}

static void EOS_CALL ajb_simple_lobby_callback(const EOS_Lobby_LeaveLobbyCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    state->done = 1;
}

static void EOS_CALL ajb_destroy_lobby_callback(const EOS_Lobby_DestroyLobbyCallbackInfo* data) {
    ajb_callback_state* state;
    if (data == NULL || data->ClientData == NULL) return;
    state = (ajb_callback_state*)data->ClientData;
    state->result = data->ResultCode;
    state->done = 1;
}

static int ajb_wait_for_callback(ajb_eos* eos, ajb_callback_state* state, const char* operation, uint32_t timeout_ms, char* error, uint32_t error_size) {
    uint32_t elapsed = 0;
    while (!state->done && elapsed < timeout_ms) {
        EOS_Platform_Tick(eos->platform);
        AJB_SLEEP_MS(5);
        elapsed += 5;
    }
    if (!state->done) {
        ajb_set_error(error, error_size, "EOS operation timed out");
        return 0;
    }
    if (state->result != EOS_EResult::EOS_Success) {
        ajb_set_result_error(error, error_size, operation, state->result);
        return 0;
    }
    return 1;
}

static int ajb_product_user_to_string(EOS_ProductUserId user, char* output, uint32_t output_size) {
    int32_t length = (int32_t)output_size;
    if (output == NULL || output_size == 0 || user == NULL) return 0;
    return EOS_ProductUserId_ToString(user, output, &length) == EOS_EResult::EOS_Success;
}

static EOS_HLobbyDetails ajb_copy_lobby_details(ajb_eos* eos, const char* lobby_id, char* error, uint32_t error_size) {
    EOS_HLobby lobby = EOS_Platform_GetLobbyInterface(eos->platform);
    EOS_Lobby_CopyLobbyDetailsHandleOptions options = {};
    EOS_HLobbyDetails details = NULL;
    options.ApiVersion = EOS_LOBBY_COPYLOBBYDETAILSHANDLE_API_LATEST;
    options.LobbyId = lobby_id;
    options.LocalUserId = eos->local_user;
    EOS_EResult result = EOS_Lobby_CopyLobbyDetailsHandle(lobby, &options, &details);
    if (result != EOS_EResult::EOS_Success || details == NULL) {
        ajb_set_result_error(error, error_size, "copy EOS lobby details", result);
        return NULL;
    }
    return details;
}

static int ajb_is_lobby_member(ajb_eos* eos, EOS_ProductUserId remote_user) {
    char error[256] = {};
    EOS_HLobbyDetails details = ajb_copy_lobby_details(eos, eos->lobby_id, error, sizeof(error));
    if (details == NULL) return 0;

    EOS_LobbyDetails_GetMemberCountOptions count_options = {};
    count_options.ApiVersion = EOS_LOBBYDETAILS_GETMEMBERCOUNT_API_LATEST;
    uint32_t count = EOS_LobbyDetails_GetMemberCount(details, &count_options);
    int found = 0;
    for (uint32_t index = 0; index < count; ++index) {
        EOS_LobbyDetails_GetMemberByIndexOptions member_options = {};
        member_options.ApiVersion = EOS_LOBBYDETAILS_GETMEMBERBYINDEX_API_LATEST;
        member_options.MemberIndex = index;
        EOS_ProductUserId member = EOS_LobbyDetails_GetMemberByIndex(details, &member_options);
        if (member != NULL && EOS_ProductUserId_IsValid(member) && EOS_ProductUserId_IsValid(remote_user)) {
            char member_id[EOS_PRODUCTUSERID_MAX_LENGTH + 1] = {};
            char remote_id[EOS_PRODUCTUSERID_MAX_LENGTH + 1] = {};
            if (ajb_product_user_to_string(member, member_id, sizeof(member_id)) &&
                ajb_product_user_to_string(remote_user, remote_id, sizeof(remote_id)) &&
                strcmp(member_id, remote_id) == 0) {
                found = 1;
                break;
            }
        }
    }
    EOS_LobbyDetails_Release(details);
    return found;
}

static int ajb_subscribe_connection_requests(ajb_eos* eos, char* error, uint32_t error_size) {
    EOS_P2P_SocketId socket_id = {};
    EOS_P2P_AddNotifyPeerConnectionRequestOptions options = {};
    socket_id.ApiVersion = EOS_P2P_SOCKETID_API_LATEST;
    snprintf(socket_id.SocketName, sizeof(socket_id.SocketName), "%s", AJB_SOCKET_NAME);
    options.ApiVersion = EOS_P2P_ADDNOTIFYPEERCONNECTIONREQUEST_API_LATEST;
    options.LocalUserId = eos->local_user;
    options.SocketId = &socket_id;
    eos->request_notification = EOS_P2P_AddNotifyPeerConnectionRequest(
        EOS_Platform_GetP2PInterface(eos->platform), &options, eos, ajb_connection_request_callback);
    if (eos->request_notification == EOS_INVALID_NOTIFICATIONID) {
        ajb_set_error(error, error_size, "subscribe to EOS P2P connection requests");
        return 0;
    }
    return 1;
}

static int ajb_subscribe_member_status(ajb_eos* eos, char* error, uint32_t error_size) {
    EOS_Lobby_AddNotifyLobbyMemberStatusReceivedOptions options = {};
    options.ApiVersion = EOS_LOBBY_ADDNOTIFYLOBBYMEMBERSTATUSRECEIVED_API_LATEST;
    eos->member_status_notification = EOS_Lobby_AddNotifyLobbyMemberStatusReceived(
        EOS_Platform_GetLobbyInterface(eos->platform), &options, eos, ajb_lobby_member_status_callback);
    if (eos->member_status_notification == EOS_INVALID_NOTIFICATIONID) {
        ajb_set_error(error, error_size, "subscribe to EOS lobby member status updates");
        return 0;
    }
    return 1;
}

static void EOS_CALL ajb_lobby_member_status_callback(const EOS_Lobby_LobbyMemberStatusReceivedCallbackInfo* data) {
    if (data == NULL || data->ClientData == NULL || data->LobbyId == NULL) return;
    ajb_eos* eos = (ajb_eos*)data->ClientData;
    if (strcmp(data->LobbyId, eos->lobby_id) != 0) return;

    const char* event = NULL;
    switch (data->CurrentStatus) {
        case EOS_ELobbyMemberStatus::EOS_LMS_JOINED:
            event = "player joined EOS lobby";
            break;
        case EOS_ELobbyMemberStatus::EOS_LMS_LEFT:
            event = "player left EOS lobby";
            break;
        case EOS_ELobbyMemberStatus::EOS_LMS_DISCONNECTED:
            event = "player disconnected from EOS lobby";
            break;
        case EOS_ELobbyMemberStatus::EOS_LMS_KICKED:
            event = "player was kicked from EOS lobby";
            break;
        case EOS_ELobbyMemberStatus::EOS_LMS_PROMOTED:
            event = "player promoted to EOS lobby owner";
            break;
        case EOS_ELobbyMemberStatus::EOS_LMS_CLOSED:
            event = "player removed because EOS lobby closed";
            break;
        default:
            return;
    }
    ajb_log_peer_event(event, data->TargetUserId);
}

static void EOS_CALL ajb_connection_request_callback(const EOS_P2P_OnIncomingConnectionRequestInfo* data) {
    if (data == NULL || data->ClientData == NULL || data->SocketId == NULL) return;
    ajb_eos* eos = (ajb_eos*)data->ClientData;
    if (strcmp(data->SocketId->SocketName, AJB_SOCKET_NAME) != 0) return;
    if (!EOS_ProductUserId_IsValid(data->RemoteUserId) || !ajb_is_lobby_member(eos, data->RemoteUserId)) {
        ajb_log_peer_event("rejected P2P connection request from non-member", data->RemoteUserId);
        return;
    }
    ajb_log_peer_event("player is attempting a P2P connection", data->RemoteUserId);

    EOS_P2P_AcceptConnectionOptions options = {};
    options.ApiVersion = EOS_P2P_ACCEPTCONNECTION_API_LATEST;
    options.LocalUserId = eos->local_user;
    options.RemoteUserId = data->RemoteUserId;
    options.SocketId = data->SocketId;
    (void)EOS_P2P_AcceptConnection(EOS_Platform_GetP2PInterface(eos->platform), &options);
}

static int ajb_enable_relay_only(ajb_eos* eos, char* error, uint32_t error_size) {
    EOS_P2P_SetRelayControlOptions set_options = {};
    set_options.ApiVersion = EOS_P2P_SETRELAYCONTROL_API_LATEST;
    set_options.RelayControl = EOS_ERelayControl::EOS_RC_ForceRelays;
    EOS_HP2P p2p = EOS_Platform_GetP2PInterface(eos->platform);
    EOS_EResult result = EOS_P2P_SetRelayControl(p2p, &set_options);
    if (result != EOS_EResult::EOS_Success) {
        ajb_set_result_error(error, error_size, "force EOS relay-only networking", result);
        return 0;
    }

    EOS_P2P_GetRelayControlOptions get_options = {};
    EOS_ERelayControl control = EOS_ERelayControl::EOS_RC_NoRelays;
    get_options.ApiVersion = EOS_P2P_GETRELAYCONTROL_API_LATEST;
    result = EOS_P2P_GetRelayControl(p2p, &get_options, &control);
    if (result != EOS_EResult::EOS_Success || control != EOS_ERelayControl::EOS_RC_ForceRelays) {
        ajb_set_error(error, error_size, "EOS did not confirm relay-only networking");
        return 0;
    }
    return 1;
}

static int ajb_login(ajb_eos* eos, char* error, uint32_t error_size) {
    EOS_HConnect connect = EOS_Platform_GetConnectInterface(eos->platform);
    EOS_Connect_CreateDeviceIdOptions device_options = {};
    ajb_callback_state device_state = {};
    device_options.ApiVersion = EOS_CONNECT_CREATEDEVICEID_API_LATEST;
    device_options.DeviceModel = "AJB Sidecar";
    EOS_Connect_CreateDeviceId(connect, &device_options, &device_state, ajb_device_id_callback);
    if (!ajb_wait_for_callback(eos, &device_state, "create EOS device ID", AJB_OPERATION_TIMEOUT_MS, error, error_size) &&
        device_state.result != EOS_EResult::EOS_DuplicateNotAllowed) {
        return 0;
    }

    EOS_Connect_Credentials credentials = {};
    credentials.ApiVersion = EOS_CONNECT_CREDENTIALS_API_LATEST;
    credentials.Type = EOS_EExternalCredentialType::EOS_ECT_DEVICEID_ACCESS_TOKEN;
    credentials.Token = NULL;

    EOS_Connect_LoginOptions login_options = {};
    EOS_Connect_UserLoginInfo user_login_info = {};
    ajb_callback_state login_state = {};
    login_options.ApiVersion = EOS_CONNECT_LOGIN_API_LATEST;
    login_options.Credentials = &credentials;
    user_login_info.ApiVersion = EOS_CONNECT_USERLOGININFO_API_LATEST;
    user_login_info.DisplayName = "AJB Player";
    login_options.UserLoginInfo = &user_login_info;
    EOS_Connect_Login(connect, &login_options, &login_state, ajb_connect_login_callback);

    if (!ajb_wait_for_callback(eos, &login_state, "EOS Connect device ID login", AJB_OPERATION_TIMEOUT_MS, error, error_size)) {
        if (login_state.result != EOS_EResult::EOS_InvalidUser || login_state.continuance_token == NULL) {
            return 0;
        }

        EOS_Connect_CreateUserOptions create_options = {};
        ajb_callback_state create_state = {};
        create_options.ApiVersion = EOS_CONNECT_CREATEUSER_API_LATEST;
        create_options.ContinuanceToken = login_state.continuance_token;
        EOS_Connect_CreateUser(connect, &create_options, &create_state, ajb_create_user_callback);
        if (!ajb_wait_for_callback(eos, &create_state, "create EOS Connect user", AJB_OPERATION_TIMEOUT_MS, error, error_size)) return 0;
        eos->local_user = create_state.product_user;
    } else {
        eos->local_user = login_state.product_user;
    }

    if (!EOS_ProductUserId_IsValid(eos->local_user)) {
        ajb_set_error(error, error_size, "EOS Connect login returned an invalid product user ID");
        return 0;
    }
    return ajb_enable_relay_only(eos, error, error_size);
}

ajb_eos* ajb_eos_create(
    const char* product_name,
    const char* product_id,
    const char* sandbox_id,
    const char* deployment_id,
    const char* client_id,
    const char* client_secret,
    const char* cache_directory,
    char* error,
    uint32_t error_size) {
    EOS_InitializeOptions initialize_options = {};
    EOS_Platform_Options platform_options = {};
    ajb_eos* eos = NULL;

    if (product_name == NULL || product_id == NULL || sandbox_id == NULL || deployment_id == NULL ||
        client_id == NULL || client_secret == NULL || cache_directory == NULL) {
        ajb_set_error(error, error_size, "EOS initialization options are incomplete");
        return NULL;
    }

    initialize_options.ApiVersion = EOS_INITIALIZE_API_LATEST;
    initialize_options.ProductName = product_name;
    initialize_options.ProductVersion = "0.1.0";
    EOS_EResult result = EOS_Initialize(&initialize_options);
    if (result != EOS_EResult::EOS_Success) {
        ajb_set_result_error(error, error_size, "initialize EOS SDK", result);
        return NULL;
    }

    eos = (ajb_eos*)calloc(1, sizeof(ajb_eos));
    if (eos == NULL) {
        (void)EOS_Shutdown();
        ajb_set_error(error, error_size, "allocate EOS runtime");
        return NULL;
    }
    eos->initialized = 1;
    eos->request_notification = EOS_INVALID_NOTIFICATIONID;
    eos->member_status_notification = EOS_INVALID_NOTIFICATIONID;

    EOS_Platform_ClientCredentials client_credentials = {};
    client_credentials.ClientId = client_id;
    client_credentials.ClientSecret = client_secret;
    platform_options.ApiVersion = EOS_PLATFORM_OPTIONS_API_LATEST;
    platform_options.ProductId = product_id;
    platform_options.SandboxId = sandbox_id;
    platform_options.DeploymentId = deployment_id;
    platform_options.ClientCredentials = client_credentials;
    platform_options.bIsServer = EOS_FALSE;
    platform_options.Flags = EOS_PF_DISABLE_OVERLAY | EOS_PF_DISABLE_SOCIAL_OVERLAY;
    platform_options.CacheDirectory = cache_directory;

    eos->platform = EOS_Platform_Create(&platform_options);
    if (eos->platform == NULL) {
        ajb_set_error(error, error_size, "create EOS platform; verify the product, sandbox, deployment, and client credentials");
        ajb_eos_close(eos);
        return NULL;
    }
    if (!ajb_login(eos, error, error_size)) {
        ajb_eos_close(eos);
        return NULL;
    }
    return eos;
}

int ajb_eos_create_lobby(ajb_eos* eos, char* lobby_id, uint32_t lobby_id_size, char* error, uint32_t error_size) {
    EOS_HLobby lobby = EOS_Platform_GetLobbyInterface(eos->platform);
    EOS_Lobby_CreateLobbyOptions options = {};
    ajb_callback_state state = {};
    options.ApiVersion = EOS_LOBBY_CREATELOBBY_API_LATEST;
    options.LocalUserId = eos->local_user;
    options.MaxLobbyMembers = 24;
    options.PermissionLevel = EOS_ELobbyPermissionLevel::EOS_LPL_PUBLICADVERTISED;
    options.bPresenceEnabled = EOS_FALSE;
    options.bAllowInvites = EOS_FALSE;
    options.BucketId = "AJB_RELAY_V1";
    options.bDisableHostMigration = EOS_TRUE;
    options.bEnableRTCRoom = EOS_FALSE;
    options.LocalRTCOptions = NULL;
    options.LobbyId = NULL;
    options.bEnableJoinById = EOS_TRUE;
    options.bRejoinAfterKickRequiresInvite = EOS_TRUE;
    options.AllowedPlatformIds = NULL;
    options.AllowedPlatformIdsCount = 0;
    options.bCrossplayOptOut = EOS_FALSE;
    options.RTCRoomJoinActionType = EOS_ELobbyRTCRoomJoinActionType::EOS_LRRJAT_ManualJoin;

    EOS_Lobby_CreateLobby(lobby, &options, &state, ajb_create_lobby_callback);
    if (!ajb_wait_for_callback(eos, &state, "create EOS lobby", AJB_OPERATION_TIMEOUT_MS, error, error_size)) return 0;
    if (state.string_result[0] == '\0' || strlen(state.string_result) >= sizeof(eos->lobby_id) ||
        strlen(state.string_result) >= lobby_id_size) {
        ajb_set_error(error, error_size, "EOS returned an invalid or oversized lobby ID");
        return 0;
    }
    snprintf(eos->lobby_id, sizeof(eos->lobby_id), "%s", state.string_result);
    snprintf(lobby_id, lobby_id_size, "%s", eos->lobby_id);
    eos->owns_lobby = 1;
    if (!ajb_subscribe_connection_requests(eos, error, error_size)) {
        ajb_leave_lobby(eos);
        return 0;
    }
    if (!ajb_subscribe_member_status(eos, error, error_size)) {
        ajb_leave_lobby(eos);
        return 0;
    }
    return 1;
}

int ajb_eos_join_lobby(ajb_eos* eos, const char* lobby_id, char* owner_id, uint32_t owner_id_size, char* error, uint32_t error_size) {
    EOS_HLobby lobby = EOS_Platform_GetLobbyInterface(eos->platform);
    EOS_Lobby_JoinLobbyByIdOptions options = {};
    ajb_callback_state state = {};
    options.ApiVersion = EOS_LOBBY_JOINLOBBYBYID_API_LATEST;
    options.LobbyId = lobby_id;
    options.LocalUserId = eos->local_user;
    options.bPresenceEnabled = EOS_FALSE;
    options.LocalRTCOptions = NULL;
    options.bCrossplayOptOut = EOS_FALSE;
    options.RTCRoomJoinActionType = EOS_ELobbyRTCRoomJoinActionType::EOS_LRRJAT_ManualJoin;

    EOS_Lobby_JoinLobbyById(lobby, &options, &state, ajb_join_lobby_callback);
    if (!ajb_wait_for_callback(eos, &state, "join EOS lobby", AJB_OPERATION_TIMEOUT_MS, error, error_size)) return 0;
    if (state.string_result[0] == '\0' || strlen(state.string_result) >= sizeof(eos->lobby_id)) {
        ajb_set_error(error, error_size, "EOS returned an invalid lobby ID after joining");
        return 0;
    }
    snprintf(eos->lobby_id, sizeof(eos->lobby_id), "%s", state.string_result);

    EOS_HLobbyDetails details = ajb_copy_lobby_details(eos, eos->lobby_id, error, error_size);
    if (details == NULL) return 0;
    EOS_LobbyDetails_CopyInfoOptions info_options = {};
    EOS_LobbyDetails_Info* info = NULL;
    info_options.ApiVersion = EOS_LOBBYDETAILS_COPYINFO_API_LATEST;
    EOS_EResult result = EOS_LobbyDetails_CopyInfo(details, &info_options, &info);
    if (result != EOS_EResult::EOS_Success || info == NULL) {
        EOS_LobbyDetails_Release(details);
        ajb_set_result_error(error, error_size, "read EOS lobby owner", result);
        return 0;
    }
    int success = ajb_product_user_to_string(info->LobbyOwnerUserId, owner_id, owner_id_size);
    EOS_LobbyDetails_Info_Release(info);
    EOS_LobbyDetails_Release(details);
    if (!success) {
        ajb_set_error(error, error_size, "convert EOS lobby owner ID");
        return 0;
    }

    if (!ajb_subscribe_connection_requests(eos, error, error_size)) return 0;
    return ajb_subscribe_member_status(eos, error, error_size);
}

int ajb_eos_send(ajb_eos* eos, const char* peer_id, const void* packet, uint32_t packet_size, char* error, uint32_t error_size) {
    if (eos == NULL || peer_id == NULL || packet == NULL || packet_size == 0 || packet_size > AJB_MAX_PACKET_SIZE) {
        ajb_set_error(error, error_size, "invalid EOS P2P packet parameters");
        return 0;
    }
    EOS_ProductUserId remote_user = EOS_ProductUserId_FromString(peer_id);
    if (!EOS_ProductUserId_IsValid(remote_user)) {
        ajb_set_error(error, error_size, "invalid EOS peer product user ID");
        return 0;
    }

    EOS_P2P_SocketId socket_id = {};
    EOS_P2P_SendPacketOptions options = {};
    socket_id.ApiVersion = EOS_P2P_SOCKETID_API_LATEST;
    snprintf(socket_id.SocketName, sizeof(socket_id.SocketName), "%s", AJB_SOCKET_NAME);
    options.ApiVersion = EOS_P2P_SENDPACKET_API_LATEST;
    options.LocalUserId = eos->local_user;
    options.RemoteUserId = remote_user;
    options.SocketId = &socket_id;
    options.Channel = AJB_P2P_CHANNEL;
    options.DataLengthBytes = packet_size;
    options.Data = packet;
    options.bAllowDelayedDelivery = EOS_TRUE;
    options.Reliability = EOS_EPacketReliability::EOS_PR_UnreliableUnordered;
    options.bDisableAutoAcceptConnection = EOS_FALSE;

    std::lock_guard<std::mutex> lock(eos->api_mutex);
    EOS_EResult result = EOS_P2P_SendPacket(EOS_Platform_GetP2PInterface(eos->platform), &options);
    if (result != EOS_EResult::EOS_Success) {
        ajb_set_result_error(error, error_size, "send EOS P2P packet", result);
        return 0;
    }
    return 1;
}

int ajb_eos_receive(ajb_eos* eos, char* peer_id, uint32_t peer_id_size, void* packet, uint32_t packet_capacity, uint32_t* packet_size, uint32_t timeout_ms, char* error, uint32_t error_size) {
    if (eos == NULL || peer_id == NULL || peer_id_size == 0 || packet == NULL || packet_size == NULL) {
        ajb_set_error(error, error_size, "invalid EOS P2P receive parameters");
        return -1;
    }
    uint32_t elapsed = 0;
    while (elapsed <= timeout_ms) {
        std::unique_lock<std::mutex> lock(eos->api_mutex);
        EOS_Platform_Tick(eos->platform);

        EOS_P2P_GetNextReceivedPacketSizeOptions size_options = {};
        uint8_t requested_channel = AJB_P2P_CHANNEL;
        size_options.ApiVersion = EOS_P2P_GETNEXTRECEIVEDPACKETSIZE_API_LATEST;
        size_options.LocalUserId = eos->local_user;
        size_options.RequestedChannel = &requested_channel;
        uint32_t next_size = 0;
        EOS_HP2P p2p = EOS_Platform_GetP2PInterface(eos->platform);
        EOS_EResult result = EOS_P2P_GetNextReceivedPacketSize(p2p, &size_options, &next_size);
        if (result == EOS_EResult::EOS_Success) {
            if (next_size > packet_capacity) {
                lock.unlock();
                ajb_set_error(error, error_size, "EOS packet exceeds receive buffer");
                return -1;
            }
            EOS_P2P_ReceivePacketOptions receive_options = {};
            EOS_ProductUserId remote_user = NULL;
            EOS_P2P_SocketId socket_id = {};
            uint8_t channel = 0;
            uint32_t bytes_written = 0;
            receive_options.ApiVersion = EOS_P2P_RECEIVEPACKET_API_LATEST;
            receive_options.LocalUserId = eos->local_user;
            receive_options.MaxDataSizeBytes = packet_capacity;
            receive_options.RequestedChannel = &requested_channel;
            result = EOS_P2P_ReceivePacket(p2p, &receive_options, &remote_user, &socket_id, &channel, packet, &bytes_written);
            lock.unlock();
            if (result != EOS_EResult::EOS_Success) {
                ajb_set_result_error(error, error_size, "receive EOS P2P packet", result);
                return -1;
            }
            if (channel != AJB_P2P_CHANNEL || strcmp(socket_id.SocketName, AJB_SOCKET_NAME) != 0 ||
                !EOS_ProductUserId_IsValid(remote_user) || !ajb_is_lobby_member(eos, remote_user)) {
                if (elapsed >= timeout_ms) break;
                AJB_SLEEP_MS(5);
                elapsed += 5;
                continue;
            }
            if (!ajb_product_user_to_string(remote_user, peer_id, peer_id_size)) {
                ajb_set_error(error, error_size, "convert incoming EOS peer ID");
                return -1;
            }
            *packet_size = bytes_written;
            return 1;
        }
        lock.unlock();

        if (result != EOS_EResult::EOS_NotFound) {
            ajb_set_result_error(error, error_size, "check EOS P2P receive queue", result);
            return -1;
        }
        if (elapsed >= timeout_ms) break;
        AJB_SLEEP_MS(5);
        elapsed += 5;
    }
    return 0;
}

static void ajb_leave_lobby(ajb_eos* eos) {
    if (eos->lobby_id[0] == '\0' || eos->local_user == NULL || eos->platform == NULL) return;
    ajb_callback_state state = {};
    EOS_HLobby lobby = EOS_Platform_GetLobbyInterface(eos->platform);
    if (eos->owns_lobby) {
        EOS_Lobby_DestroyLobbyOptions options = {};
        options.ApiVersion = EOS_LOBBY_DESTROYLOBBY_API_LATEST;
        options.LocalUserId = eos->local_user;
        options.LobbyId = eos->lobby_id;
        EOS_Lobby_DestroyLobby(lobby, &options, &state, ajb_destroy_lobby_callback);
    } else {
        EOS_Lobby_LeaveLobbyOptions options = {};
        options.ApiVersion = EOS_LOBBY_LEAVELOBBY_API_LATEST;
        options.LocalUserId = eos->local_user;
        options.LobbyId = eos->lobby_id;
        EOS_Lobby_LeaveLobby(lobby, &options, &state, ajb_simple_lobby_callback);
    }
    char ignored[128] = {};
    (void)ajb_wait_for_callback(eos, &state, "leave EOS lobby", 10000, ignored, sizeof(ignored));
    eos->lobby_id[0] = '\0';
}

void ajb_eos_close(ajb_eos* eos) {
    if (eos == NULL) return;
    if (eos->platform != NULL) {
        if (eos->request_notification != EOS_INVALID_NOTIFICATIONID) {
            EOS_P2P_RemoveNotifyPeerConnectionRequest(EOS_Platform_GetP2PInterface(eos->platform), eos->request_notification);
        }
        if (eos->member_status_notification != EOS_INVALID_NOTIFICATIONID) {
            EOS_Lobby_RemoveNotifyLobbyMemberStatusReceived(EOS_Platform_GetLobbyInterface(eos->platform), eos->member_status_notification);
        }
        ajb_leave_lobby(eos);
        EOS_Platform_Release(eos->platform);
        eos->platform = NULL;
    }
    if (eos->initialized) {
        (void)EOS_Shutdown();
    }
    free(eos);
}
