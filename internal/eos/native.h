#ifndef AJB_EOS_NATIVE_H
#define AJB_EOS_NATIVE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ajb_eos ajb_eos;

ajb_eos* ajb_eos_create(
    const char* product_name,
    const char* product_id,
    const char* sandbox_id,
    const char* deployment_id,
    const char* client_id,
    const char* client_secret,
    const char* cache_directory,
    char* error,
    uint32_t error_size);

int ajb_eos_create_lobby(ajb_eos* eos, char* lobby_id, uint32_t lobby_id_size, char* error, uint32_t error_size);
int ajb_eos_join_lobby(ajb_eos* eos, const char* lobby_id, char* owner_id, uint32_t owner_id_size, char* error, uint32_t error_size);
int ajb_eos_send(ajb_eos* eos, const char* peer_id, const void* packet, uint32_t packet_size, char* error, uint32_t error_size);
int ajb_eos_receive(ajb_eos* eos, char* peer_id, uint32_t peer_id_size, void* packet, uint32_t packet_capacity, uint32_t* packet_size, uint32_t timeout_ms, char* error, uint32_t error_size);
void ajb_eos_close(ajb_eos* eos);

#ifdef __cplusplus
}
#endif

#endif
