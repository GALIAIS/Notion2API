#ifndef WREQ_FFI_COMPAT_H
#define WREQ_FFI_COMPAT_H

#include <stdint.h>
#include <stddef.h>

typedef struct WreqClient WreqClient;
typedef struct WreqResponseHandle WreqResponseHandle;

int32_t wreq_request_begin(struct WreqClient *client,
                           const uint8_t *spec_json,
                           size_t spec_len,
                           const uint8_t *body_ptr,
                           size_t body_len,
                           struct WreqResponseHandle **out_handle,
                           uint16_t *out_status,
                           char **out_headers_json,
                           char **out_final_url,
                           char **out_error);
intptr_t wreq_response_read(struct WreqResponseHandle *handle,
                            uint8_t *buf,
                            size_t cap,
                            uint32_t timeout_ms);
void wreq_response_close(struct WreqResponseHandle *handle);

#endif /* WREQ_FFI_COMPAT_H */
